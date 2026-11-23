package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ErrRelease reports a release gate that cannot be evaluated, as opposed to
// one that is evaluated and found unmet (releaseReport.Problems).
var ErrRelease = errors.New("release gate")

const (
	registryFile   = "docs/verification.md"
	registryHeader = "| ID | Source | Gate | Tier | Evidence | Status |"
	gateRelease    = "release"
	tierLiveName   = "live"
	statusDoneName = "Done"
	resultPass     = "pass"
	resultFail     = "fail"
)

// recordPattern is the path shape of verification §6:
// quality/records/<ID>/<date>-<os>.json. internal/arch checks the same shape
// for `Done` live rows; both read it from the registry's Evidence column.
var recordPattern = regexp.MustCompile(`quality/records/([A-Za-z0-9-]+)/(\d{4}-\d{2}-\d{2})-(linux|darwin)\.json`)

var hexRevision = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// readIn reads a slash-separated path below dir; it cannot leave dir.
func readIn(dir, rel string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	data, readErr := root.ReadFile(filepath.FromSlash(rel))
	closeErr := root.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", rel, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %s: %w", dir, closeErr)
	}
	return data, nil
}

// registryRow is one row of the registry table of docs/verification.md.
type registryRow struct {
	ID       string
	Source   string
	Gate     string
	Tier     string
	Evidence string
	Status   string
}

// liveRecord is the schema of verification §6. Versions are strings or
// integers in the registry's history, so they stay raw until validated.
type liveRecord struct {
	ID              string          `json:"id"`
	Check           string          `json:"check"`
	Revision        string          `json:"revision"`
	FugueVersion    string          `json:"fugue_version"`
	SchemaVersion   json.RawMessage `json:"schema_version"`
	ProtocolVersion json.RawMessage `json:"protocol_version"`
	OS              string          `json:"os"`
	Arch            string          `json:"arch"`
	StartedAt       string          `json:"started_at"`
	FinishedAt      string          `json:"finished_at"`
	Result          string          `json:"result"`
	Measurements    map[string]any  `json:"measurements"`
	Covers          []string        `json:"covers"`
	Notes           *string         `json:"notes"`
}

// liveEntry is a record that a live row names, with the path it was read from.
type liveEntry struct {
	Row    registryRow
	Path   string
	Record liveRecord
}

// releaseReport is the outcome of one evaluation of the release gate against
// a release candidate: every release-gate row, the live records read, and the
// precise list of what blocks the release.
type releaseReport struct {
	RC       string
	Rows     []registryRow
	Live     []liveEntry
	Problems []string
}

// parseRegistry reads the registry table of docs/verification.md. Rows of the
// "Not in stage A" table have a different header and are not part of it.
func parseRegistry(md string) ([]registryRow, error) {
	var rows []registryRow
	inTable := false
	for line := range strings.SplitSeq(md, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == registryHeader:
			inTable = true
		case !inTable:
		case !strings.HasPrefix(line, "|"):
			inTable = false
		case strings.HasPrefix(line, "| ---"):
		default:
			row, err := parseRegistryRow(line)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s has no registry table", ErrRelease, registryFile)
	}
	return rows, nil
}

func parseRegistryRow(line string) (registryRow, error) {
	cells := strings.Split(strings.Trim(line, "|"), "|")
	if len(cells) != 6 {
		return registryRow{}, fmt.Errorf("%w: malformed registry row %q", ErrRelease, line)
	}
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return registryRow{
		ID:       strings.Trim(cells[0], "`"),
		Source:   strings.Trim(cells[1], "`"),
		Gate:     strings.Trim(cells[2], "`"),
		Tier:     strings.Trim(cells[3], "`"),
		Evidence: cells[4],
		Status:   strings.Trim(cells[5], "`"),
	}, nil
}

// checkRelease evaluates verification §7 step 2 for rc in the repository at
// dir: every release-gate row is Done, and every record a live row names is
// valid for rc. rc must be the checked-out commit, otherwise the tests that ran
// say nothing about it.
func checkRelease(ctx context.Context, dir, rc string) (*releaseReport, error) {
	full, err := resolveCommit(ctx, dir, rc)
	if err != nil {
		return nil, err
	}
	head, err := resolveCommit(ctx, dir, "HEAD")
	if err != nil {
		return nil, err
	}
	report := &releaseReport{RC: full}
	if full != head {
		report.Problems = append(report.Problems,
			fmt.Sprintf("RC %s is not the checked-out commit %s: run the release gate on the RC itself", full, head))
	}
	if _, diffErr := capture(ctx, dir, "git", "diff", "--quiet", "HEAD"); diffErr != nil {
		report.Problems = append(report.Problems, "tracked files differ from HEAD: the RC must be a clean checkout")
	}
	raw, err := readIn(dir, registryFile)
	if err != nil {
		return nil, fmt.Errorf("%w: read registry: %w", ErrRelease, err)
	}
	rows, err := parseRegistry(string(raw))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Gate != gateRelease {
			continue
		}
		report.Rows = append(report.Rows, row)
		if row.Status != statusDoneName {
			// The status line already blocks the release; the record is named
			// when the row moves to Done.
			report.Problems = append(report.Problems, fmt.Sprintf("%s (%s): status is %q, want %q", row.ID, row.Tier, row.Status, statusDoneName))
			continue
		}
		if row.Tier == tierLiveName {
			entries, probs := checkLiveRow(ctx, dir, full, &row)
			report.Live = append(report.Live, entries...)
			report.Problems = append(report.Problems, probs...)
		}
	}
	return report, nil
}

// checkLiveRow validates every record the row's Evidence names against rc.
func checkLiveRow(ctx context.Context, dir, rc string, row *registryRow) ([]liveEntry, []string) {
	matches := recordPattern.FindAllStringSubmatch(row.Evidence, -1)
	if len(matches) == 0 {
		return nil, []string{fmt.Sprintf("%s: live row names no record at quality/records/%s/<date>-<os>.json", row.ID, row.ID)}
	}
	var entries []liveEntry
	var probs []string
	for _, m := range matches {
		path := m[0]
		if m[1] != row.ID {
			probs = append(probs, fmt.Sprintf("%s: record %s belongs to %s", row.ID, path, m[1]))
			continue
		}
		rec, recProbs := readRecord(dir, path, row.ID, m[3])
		if len(recProbs) == 0 {
			recProbs = recordValidAgainst(ctx, dir, rc, path, &rec)
		}
		probs = append(probs, recProbs...)
		if len(recProbs) == 0 {
			entries = append(entries, liveEntry{Row: *row, Path: path, Record: rec})
		}
	}
	return entries, probs
}

// readRecord parses and schema-checks one record file. osName is the OS the
// file name declares; the record must agree.
func readRecord(dir, path, id, osName string) (liveRecord, []string) {
	raw, err := readIn(dir, path)
	if err != nil {
		return liveRecord{}, []string{fmt.Sprintf("%s: record %s cannot be read: %v", id, path, err)}
	}
	var rec liveRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return liveRecord{}, []string{fmt.Sprintf("%s: record %s is invalid JSON: %v", id, path, err)}
	}
	var probs []string
	for _, p := range validateRecord(&rec) {
		probs = append(probs, fmt.Sprintf("%s: record %s: %s", id, path, p))
	}
	if rec.ID != id {
		probs = append(probs, fmt.Sprintf("%s: record %s has id %q", id, path, rec.ID))
	}
	if rec.OS != osName {
		probs = append(probs, fmt.Sprintf("%s: record %s has os %q, its file name says %q", id, path, rec.OS, osName))
	}
	if leak := secretFindings(string(raw)); len(leak) > 0 {
		probs = append(probs, fmt.Sprintf("%s: record %s contains %s", id, path, strings.Join(leak, ", ")))
	}
	return rec, probs
}

// validateRecord checks the fields of verification §6 that do not need git.
func validateRecord(rec *liveRecord) []string {
	probs := validateRecordStrings(rec)
	for name, val := range map[string]json.RawMessage{"schema_version": rec.SchemaVersion, "protocol_version": rec.ProtocolVersion} {
		if !validVersion(val) {
			probs = append(probs, fmt.Sprintf("field %q is missing or empty", name))
		}
	}
	probs = append(probs, validateRecordTimes(rec)...)
	if rec.Result != resultPass && rec.Result != resultFail {
		probs = append(probs, fmt.Sprintf("result %q is neither pass nor fail", rec.Result))
	}
	if rec.Measurements == nil {
		probs = append(probs, "measurements is missing")
	}
	if len(rec.Covers) == 0 {
		probs = append(probs, "covers is missing or empty")
	}
	if rec.Notes == nil {
		probs = append(probs, "notes is missing")
	}
	slices.Sort(probs)
	return probs
}

func validateRecordStrings(rec *liveRecord) []string {
	var probs []string
	for name, val := range map[string]string{
		"id": rec.ID, "check": rec.Check, "revision": rec.Revision, "fugue_version": rec.FugueVersion,
		"os": rec.OS, "arch": rec.Arch,
	} {
		if strings.TrimSpace(val) == "" {
			probs = append(probs, fmt.Sprintf("field %q is missing", name))
		}
	}
	if !hexRevision.MatchString(rec.Revision) {
		probs = append(probs, fmt.Sprintf("revision %q is not a commit hash", rec.Revision))
	}
	return probs
}

func validateRecordTimes(rec *liveRecord) []string {
	started, errStart := time.Parse(time.RFC3339, rec.StartedAt)
	finished, errFinish := time.Parse(time.RFC3339, rec.FinishedAt)
	switch {
	case errStart != nil:
		return []string{"started_at is not an RFC 3339 timestamp"}
	case errFinish != nil:
		return []string{"finished_at is not an RFC 3339 timestamp"}
	case finished.Before(started):
		return []string{"finished_at is before started_at"}
	}
	return nil
}

func validVersion(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var n int
	return json.Unmarshal(raw, &n) == nil && n > 0
}

// recordValidAgainst applies verification §6: the record passed, and it is
// still valid for rc, meaning its revision is an ancestor of rc and no file it
// covers changed between them.
func recordValidAgainst(ctx context.Context, dir, rc, path string, rec *liveRecord) []string {
	id := rec.ID
	if rec.Result != resultPass {
		return []string{fmt.Sprintf("%s: record %s has result %q, want %q", id, path, rec.Result, resultPass)}
	}
	revision, err := resolveCommit(ctx, dir, rec.Revision)
	if err != nil {
		return []string{fmt.Sprintf("%s: record %s revision %s is not a commit of this repository", id, path, rec.Revision)}
	}
	if _, ancestorErr := capture(ctx, dir, "git", "merge-base", "--is-ancestor", revision, rc); ancestorErr != nil {
		return []string{fmt.Sprintf("%s: record %s revision %s is not an ancestor of the RC", id, path, revision)}
	}
	args := []string{"diff", "--name-only", revision + ".." + rc, "--"}
	for _, glob := range rec.Covers {
		args = append(args, ":(glob)"+glob)
	}
	out, err := capture(ctx, dir, "git", args...)
	if err != nil {
		return []string{fmt.Sprintf("%s: record %s covers cannot be diffed: %v", id, path, err)}
	}
	changed := strings.Fields(out)
	if len(changed) == 0 {
		return nil
	}
	shown := changed
	if len(shown) > 5 {
		shown = append(slices.Clone(shown[:5]), fmt.Sprintf("and %d more", len(changed)-5))
	}
	return []string{fmt.Sprintf("%s: record %s is stale: %d covered file(s) changed since %s (%s); run the check again",
		id, path, len(changed), revision[:12], strings.Join(shown, ", "))}
}

// resolveCommit expands ref to a full commit hash.
func resolveCommit(ctx context.Context, dir, ref string) (string, error) {
	out, err := capture(ctx, dir, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %q is not a commit: %w", ErrRelease, ref, err)
	}
	return strings.TrimSpace(out), nil
}

// secretPatterns approximate the content-safety detector for the shapes a
// record author could paste by accident: verification §6 says a record holds
// no secret, prompt, model output or token. fuguegate may not import the
// product detector (ADR 0001), so this is a conservative local list.
func secretPatterns() map[string]*regexp.Regexp {
	return map[string]*regexp.Regexp{
		"an API key":         regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		"a GitHub token":     regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		"a bearer token":     regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{16,}`),
		"a JWT":              regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.`),
		"a private key":      regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		"an email address":   regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		"an assigned secret": regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|token)\s*[:=]\s*\S{6,}`),
	}
}

// secretFindings names the kinds of secret text found in s, sorted.
func secretFindings(s string) []string {
	var found []string
	for name, re := range secretPatterns() {
		if re.MatchString(s) {
			found = append(found, name)
		}
	}
	slices.Sort(found)
	return found
}
