package arch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	verificationFile  = "docs/verification.md"
	statusDone        = "Done"
	tierPR            = "pr"
	tierNightly       = "nightly"
	tierRelease       = "release"
	tierLive          = "live"
	fieldStartedAt    = "started_at"
	fieldFinishedAt   = "finished_at"
	fieldResult       = "result"
	verifRepoCase     = "repo"
	verifCleanFixture = "clean"
)

var (
	probePatchRef  = regexp.MustCompile(`quality/probes/([a-zA-Z0-9._-]+)\.patch`)
	probeNamedRef  = regexp.MustCompile(`\bprobe:\s*([a-zA-Z0-9._-]+)`)
	liveRecordRef  = regexp.MustCompile(`quality/records/([A-Za-z0-9-]+)/(\d{4}-\d{2}-\d{2})-(linux|darwin)\.json`)
	verifIDPattern = regexp.MustCompile(`^([A-Z]+-[A-Z]?)(\d{2,})$`)
)

type verificationRow struct {
	id       string
	source   string
	gate     string
	tier     string
	evidence string
	status   string
}

func seededPrefixCounts() map[string]int {
	return map[string]int{
		"VR-E": 14,
		"TR-E": 8,
		"DA-E": 8,
		"A-S":  3,
	}
}

func TestVerificationRegistryMatchesSeededCounts(t *testing.T) {
	t.Parallel()
	r := repo{root: repoRoot}
	counts, probs, err := parseVerificationCounts(r)
	if err != nil {
		t.Fatal(err)
	}
	expectNone(t, probs)
	want := seededPrefixCounts()
	if len(counts) != len(want) {
		t.Fatalf("prefix count mismatch: got %v, want %v", counts, want)
	}
	for prefix, wantCount := range want {
		if gotCount, ok := counts[prefix]; !ok || gotCount != wantCount {
			t.Errorf("prefix %s: got %d (present=%v), want %d", prefix, gotCount, ok, wantCount)
		}
	}
}

func TestVerificationEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		repo repo
		want []string
	}{
		{name: verifRepoCase, repo: repo{root: repoRoot}},
		{name: verifCleanFixture, repo: fixture("verification-good")},
		{
			name: "gap",
			repo: fixture("verification-gap"),
			want: []string{"VR-E02 is missing (gap in VR-E 01..03)"},
		},
		{
			name: "duplicate",
			repo: fixture("verification-duplicate"),
			want: []string{"VR-E02 appears 2 times"},
		},
		{
			name: "missing test",
			repo: fixture("verification-missing-test"),
			want: []string{"VR-E01 is Done but its evidence TestMissing does not exist"},
		},
		{
			name: "one os",
			repo: fixture("verification-one-os"),
			want: []string{"VR-E01 is Done but its evidence TestLinuxOnly does not build for GOOS=darwin"},
		},
		{
			name: "missing probe patch",
			repo: fixture("verification-missing-probe"),
			want: []string{"VR-E01 names probe missing-check but quality/probes/missing-check.patch does not exist"},
		},
		{
			name: "malformed live record",
			repo: fixture("verification-malformed-record"),
			want: []string{"A-S01 live record quality/records/A-S01/2026-11-13-linux.json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkVerificationEvidence(t.Context(), tc.repo)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.want) > 0 {
				expectViolations(t, got, tc.want...)
			} else {
				expectNone(t, got)
			}
		})
	}
}

func checkVerificationEvidence(ctx context.Context, r repo) ([]string, error) {
	counts, out, err := parseVerificationCounts(r)
	if err != nil {
		return nil, err
	}
	skipped, skipProbs, err := parseVerificationSkipped(r)
	if err != nil {
		return nil, err
	}
	out = append(out, skipProbs...)

	rows, regProbs, err := parseVerificationRows(r)
	if err != nil {
		return nil, err
	}
	out = append(out, regProbs...)
	out = append(out, checkVerificationSequence(counts, skipped, rows)...)

	rowProbs, needOSCheck := checkVerificationRows(r, rows)
	out = append(out, rowProbs...)
	if needOSCheck {
		testProbs, err := checkTestEvidenceRows(ctx, r, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, testProbs...)
	}
	slices.Sort(out)
	return out, nil
}

func checkVerificationRows(r repo, rows []verificationRow) ([]string, bool) {
	var out []string
	var needOSCheck bool
	for i := range rows {
		row := &rows[i]
		out = append(out, checkRowProbes(r, row)...)
		if row.status != statusDone {
			continue
		}
		if row.tier == tierLive {
			out = append(out, checkLiveRow(r, row)...)
		} else if isCodeTier(row.tier) {
			needOSCheck = true
		}
	}
	return out, needOSCheck
}

func isCodeTier(tier string) bool {
	return tier == tierPR || tier == tierNightly || tier == tierRelease
}

func parseVerificationCounts(r repo) (map[string]int, []string, error) {
	rows, err := r.table(verificationFile, "| Prefix | Source | Count |")
	if err != nil {
		return nil, nil, err
	}
	counts := make(map[string]int, len(rows))
	var out []string
	for _, row := range rows {
		if len(row) < 3 {
			out = append(out, verificationFile+": malformed prefix count row")
			continue
		}
		prefix := row[0]
		n, convErr := strconv.Atoi(row[2])
		if convErr != nil || n <= 0 {
			out = append(out, fmt.Sprintf("prefix %s has invalid count %q", prefix, row[2]))
			continue
		}
		if _, exists := counts[prefix]; exists {
			out = append(out, fmt.Sprintf("prefix %s appears more than once in header table", prefix))
			continue
		}
		counts[prefix] = n
	}
	return counts, out, nil
}

func parseVerificationSkipped(r repo) (map[string]bool, []string, error) {
	rows, err := r.table(verificationFile, "| ID | Reason |")
	if errors.Is(err, errNoTable) {
		return map[string]bool{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	skipped := make(map[string]bool, len(rows))
	var out []string
	for _, row := range rows {
		if len(row) < 2 || row[0] == "" || row[1] == "" {
			out = append(out, verificationFile+": malformed Not in stage A row")
			continue
		}
		id := row[0]
		if skipped[id] {
			out = append(out, id+" appears multiple times in Not in stage A")
			continue
		}
		skipped[id] = true
	}
	return skipped, out, nil
}

func parseVerificationRows(r repo) ([]verificationRow, []string, error) {
	rows, err := r.table(verificationFile, "| ID | Source | Gate | Tier | Evidence | Status |")
	if err != nil {
		return nil, nil, err
	}
	out := make([]verificationRow, 0, len(rows))
	var probs []string
	for _, row := range rows {
		if len(row) < 6 {
			probs = append(probs, verificationFile+": malformed registry row")
			continue
		}
		vr := verificationRow{
			id:       row[0],
			source:   row[1],
			gate:     row[2],
			tier:     row[3],
			evidence: row[4],
			status:   row[5],
		}
		probs = append(probs, validateRowFields(&vr)...)
		out = append(out, vr)
	}
	return out, probs, nil
}

func validateRowFields(vr *verificationRow) []string {
	var probs []string
	if vr.gate != tierRelease && vr.gate != "stable" {
		probs = append(probs, fmt.Sprintf("%s has invalid gate %q", vr.id, vr.gate))
	}
	if !isCodeTier(vr.tier) && vr.tier != tierLive {
		probs = append(probs, fmt.Sprintf("%s has invalid tier %q", vr.id, vr.tier))
	}
	if vr.status != "Not started" && vr.status != "In progress" && vr.status != statusDone {
		probs = append(probs, fmt.Sprintf("%s has invalid status %q", vr.id, vr.status))
	}
	return probs
}

func checkVerificationSequence(counts map[string]int, skipped map[string]bool, rows []verificationRow) []string {
	var out []string
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		seen[row.id]++
		if skipped[row.id] {
			out = append(out, row.id+" appears in both registry and Not in stage A")
		}
		out = append(out, checkDeclaredID(row.id, counts)...)
	}
	for id := range skipped {
		out = append(out, checkDeclaredID(id, counts)...)
	}
	for id, count := range seen {
		if count > 1 {
			out = append(out, fmt.Sprintf("%s appears %d times in registry", id, count))
		}
	}
	for prefix, count := range counts {
		for i := 1; i <= count; i++ {
			id := fmt.Sprintf("%s%02d", prefix, i)
			if seen[id] == 0 && !skipped[id] {
				out = append(out, fmt.Sprintf("%s is missing (gap in %s 01..%02d)", id, prefix, count))
			}
		}
	}
	return out
}

func checkDeclaredID(id string, counts map[string]int) []string {
	m := verifIDPattern.FindStringSubmatch(id)
	if m == nil {
		return []string{fmt.Sprintf("invalid verification ID %q", id)}
	}
	prefix := m[1]
	num, err := strconv.Atoi(m[2])
	maxCount, ok := counts[prefix]
	if !ok {
		return []string{fmt.Sprintf("%s uses undeclared prefix %s", id, prefix)}
	}
	if err != nil || num < 1 || num > maxCount {
		return []string{fmt.Sprintf("%s is out of range for prefix %s (01..%02d)", id, prefix, maxCount)}
	}
	return nil
}

func extractProbes(evidence string) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range []*regexp.Regexp{probePatchRef, probeNamedRef} {
		for _, m := range re.FindAllStringSubmatch(evidence, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

func checkRowProbes(r repo, row *verificationRow) []string {
	var out []string
	for _, name := range extractProbes(row.evidence) {
		rel := filepath.Join("quality", "probes", name+".patch")
		info, err := os.Stat(filepath.Join(r.root, rel))
		if err != nil || info.IsDir() || info.Size() == 0 {
			out = append(out, fmt.Sprintf("%s names probe %s but quality/probes/%s.patch does not exist", row.id, name, name))
		}
	}
	return out
}

func checkTestEvidenceRows(ctx context.Context, r repo, rows []verificationRow) ([]string, error) {
	linuxTests, err := r.testFuncsForOS(ctx, "linux")
	if err != nil {
		return nil, err
	}
	darwinTests, err := r.testFuncsForOS(ctx, "darwin")
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range rows {
		row := &rows[i]
		if row.status != statusDone || !isCodeTier(row.tier) {
			continue
		}
		out = append(out, checkRowTests(row, linuxTests, darwinTests)...)
	}
	return out, nil
}

func checkRowTests(row *verificationRow, linuxTests, darwinTests map[string]bool) []string {
	tests := evidenceName.FindAllString(row.evidence, -1)
	probes := extractProbes(row.evidence)
	if len(tests) == 0 && len(probes) == 0 {
		return []string{row.id + " is Done but names no evidence"}
	}
	var out []string
	for _, name := range tests {
		inLinux, inDarwin := linuxTests[name], darwinTests[name]
		switch {
		case !inLinux && !inDarwin:
			out = append(out, fmt.Sprintf("%s is Done but its evidence %s does not exist", row.id, name))
		case !inLinux:
			out = append(out, fmt.Sprintf("%s is Done but its evidence %s does not build for GOOS=linux", row.id, name))
		case !inDarwin:
			out = append(out, fmt.Sprintf("%s is Done but its evidence %s does not build for GOOS=darwin", row.id, name))
		}
	}
	return out
}

func (r repo) testFuncsForOS(ctx context.Context, goos string) (map[string]bool, error) {
	roots := []string{r.root}
	uiMod := filepath.Join(r.root, "ui", "go.mod")
	if info, err := os.Stat(uiMod); err == nil && !info.IsDir() {
		roots = append(roots, filepath.Join(r.root, "ui"))
	}
	funcs := map[string]bool{}
	fset := token.NewFileSet()
	for _, modRoot := range roots {
		pkgs, err := listPackagesForOS(ctx, modRoot, goos)
		if err != nil {
			return nil, err
		}
		for _, p := range pkgs {
			for _, name := range slices.Concat(p.TestGoFiles, p.XTestGoFiles) {
				if err := collectTestFuncs(fset, filepath.Join(p.Dir, name), funcs); err != nil {
					return nil, err
				}
			}
		}
	}
	return funcs, nil
}

func listPackagesForOS(ctx context.Context, dir, goos string) ([]*goPackage, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOPROXY=off")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list GOOS=%s in %s: %w (%s)", goos, dir, err, strings.TrimSpace(errBuf.String()))
	}
	var pkgs []*goPackage
	dec := json.NewDecoder(&out)
	for {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return pkgs, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode go list GOOS=%s: %w", goos, err)
		}
		pkgs = append(pkgs, &p)
	}
}

func collectTestFuncs(fset *token.FileSet, path string, funcs map[string]bool) error {
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && (strings.HasPrefix(fn.Name.Name, "Test") || strings.HasPrefix(fn.Name.Name, "Fuzz")) {
			funcs[fn.Name.Name] = true
		}
	}
	return nil
}

func checkLiveRow(r repo, row *verificationRow) []string {
	matches := liveRecordRef.FindAllStringSubmatch(row.evidence, -1)
	if len(matches) == 0 {
		return []string{row.id + " is Done in tier live but names no record at quality/records/<ID>/<date>-<os>.json"}
	}
	var out []string
	for _, m := range matches {
		recPath := m[0]
		if m[1] != row.id {
			out = append(out, fmt.Sprintf("%s live record %s belongs to %s", row.id, recPath, m[1]))
			continue
		}
		raw, err := r.read(recPath)
		if err != nil {
			out = append(out, fmt.Sprintf("%s live record %s cannot be read: %v", row.id, recPath, err))
			continue
		}
		out = append(out, validateLiveRecord(row.id, recPath, raw)...)
	}
	return out
}

func validateLiveRecord(id, recPath, raw string) []string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return []string{fmt.Sprintf("%s live record %s is invalid JSON: %v", id, recPath, err)}
	}
	required := []string{
		"id", "check", "revision", "fugue_version",
		"schema_version", "protocol_version", "os", "arch",
		fieldStartedAt, fieldFinishedAt, fieldResult, "measurements",
		"covers", "notes",
	}
	var out []string
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			out = append(out, fmt.Sprintf("%s live record %s is missing required field %q", id, recPath, key))
		}
	}
	if len(out) > 0 {
		return out
	}
	out = append(out, validateLiveRecordStrings(id, recPath, fields)...)
	out = append(out, validateLiveRecordStructured(id, recPath, fields)...)
	return out
}

func validateLiveRecordStrings(id, recPath string, fields map[string]json.RawMessage) []string {
	var out []string
	for _, key := range []string{"id", "check", "revision", "fugue_version", "os", "arch", fieldStartedAt, fieldFinishedAt, fieldResult} {
		var s string
		if err := json.Unmarshal(fields[key], &s); err != nil || strings.TrimSpace(s) == "" {
			out = append(out, fmt.Sprintf("%s live record %s has invalid %q", id, recPath, key))
			continue
		}
		if msg := validateLiveStringField(id, recPath, key, s); msg != "" {
			out = append(out, msg)
		}
	}
	var notes string
	if err := json.Unmarshal(fields["notes"], &notes); err != nil {
		out = append(out, fmt.Sprintf("%s live record %s has non-string notes", id, recPath))
	}
	return out
}

func validateLiveStringField(id, recPath, key, val string) string {
	switch key {
	case "id":
		if val != id {
			return fmt.Sprintf("%s live record %s has mismatched id %q", id, recPath, val)
		}
	case fieldResult:
		if val != "pass" && val != "fail" {
			return fmt.Sprintf("%s live record %s has invalid result %q", id, recPath, val)
		}
	case fieldStartedAt, fieldFinishedAt:
		if _, err := time.Parse(time.RFC3339, val); err != nil {
			return fmt.Sprintf("%s live record %s has invalid %s timestamp", id, recPath, key)
		}
	}
	return ""
}

func validateLiveRecordStructured(id, recPath string, fields map[string]json.RawMessage) []string {
	var out []string
	for _, key := range []string{"schema_version", "protocol_version"} {
		if !validVersionField(fields[key]) {
			out = append(out, fmt.Sprintf("%s live record %s has invalid %q", id, recPath, key))
		}
	}
	var measurements map[string]any
	if err := json.Unmarshal(fields["measurements"], &measurements); err != nil || measurements == nil {
		out = append(out, fmt.Sprintf("%s live record %s has invalid measurements object", id, recPath))
	}
	var covers []string
	if err := json.Unmarshal(fields["covers"], &covers); err != nil || len(covers) == 0 {
		out = append(out, fmt.Sprintf("%s live record %s has invalid covers array", id, recPath))
	}
	return out
}

func validVersionField(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s) != ""
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n > 0
	}
	return false
}
