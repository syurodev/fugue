package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// ErrVerifyLive reports a live record that cannot be created.
var ErrVerifyLive = errors.New("verify-live failed")

const (
	osLinux  = "linux"
	osDarwin = "darwin"
)

// liveStep is one thing the owner does and confirms. A step with oses runs
// only on those operating systems.
type liveStep struct {
	text string
	oses []string
}

// liveCheck is the checklist behind one live verification ID. The product
// binary has no verification command in stage A (activation A-06), so the
// checklist lives here, in repository tooling.
type liveCheck struct {
	name         string
	covers       []string
	steps        []liveStep
	measurements []string
	oses         []string
}

// liveChecks are the release-gate live rows of docs/verification.md. The
// covers are the code each check exercises: a change under them makes the
// record stale for a later RC (verification §6), so they err wide.
func liveChecks() map[string]*liveCheck {
	return map[string]*liveCheck{
		"VR-E04": {
			name:   "approval-relay",
			covers: []string{"internal/vendor/**", "internal/vendoradapter/**", "internal/interaction/**"},
			steps: []liveStep{
				{text: "With a real Claude Code login, a vendor tool request reaches the approval box verbatim"},
				{text: "With a real Claude Code login, allow with a message and deny with a message both reach the vendor"},
				{text: "With a real Codex login, a vendor tool request reaches the approval box verbatim"},
				{text: "With a real Codex login, allow with a message and deny with a message both reach the vendor"},
				{text: "With a real Antigravity (agy) login, a vendor tool request reaches the approval box verbatim"},
				{text: "With a real Antigravity (agy) login, allow with a message and deny with a message both reach the vendor"},
				{text: "Fugue never answered a vendor tool request on its own, for any of the three vendors"},
			},
		},
		"DA-E01": {
			name:   "ubuntu-performance-thresholds",
			covers: []string{"ui/**", "internal/app/**", "internal/hostapi/**"},
			oses:   []string{osLinux},
			steps: []liveStep{
				{text: "You ran the desktop app on your Ubuntu machine, not in a VM or CI"},
				{text: "Every threshold of desktop-app section 9 is met on that machine"},
			},
			measurements: []string{"machine"},
		},
		"DA-E02": {
			name:   "ime-telex",
			covers: []string{"ui/**"},
			steps: []liveStep{
				{text: "Ubuntu, Wayland session, Telex over IBus: composing text shows correctly, commits correctly, backspace deletes exactly one character", oses: []string{osLinux}},
				{text: "Ubuntu, X11 session, Telex over IBus: composing text shows correctly, commits correctly, backspace deletes exactly one character", oses: []string{osLinux}},
				{text: "macOS, the system Vietnamese Telex input source: composing text shows correctly, commits correctly, backspace deletes exactly one character", oses: []string{osDarwin}},
			},
		},
		"OR-E01": {
			name:         "swarm-drill-8-agents",
			covers:       []string{"internal/teams/**", "internal/vendor/**", "internal/vendoradapter/**"},
			oses:         []string{osLinux},
			steps:        []liveStep{{text: "You ran a team of 8 agents on your Linux machine and watched the UI throughout"}, {text: "The section 7 defaults were adjusted to the result, or confirmed as they are"}},
			measurements: []string{"ram_peak_mb", "cpu_peak_percent", "ui_response_ms"},
		},
	}
}

var measurementFlag = regexp.MustCompile(`^([a-z][a-z0-9_]*)=(.+)$`)

// runVerifyLive guides the owner through a live check and writes the record of
// verification §6 at quality/records/<ID>/<date>-<os>.json. Stdin carries the
// owner's answers; the command never reads the vendor's output or any account
// detail, so a record cannot carry them.
func runVerifyLive(ctx context.Context, clk clock.Clock, args []string, stdin io.Reader, stdout io.Writer) error {
	in, err := parseVerifyLive(args)
	if err != nil {
		return err
	}
	rec, err := newRecord(ctx, in.dir, in.id, in.check, in.measurements)
	if err != nil {
		return err
	}
	rec.Notes = &in.notes
	if in.fugueVersion != "" {
		rec.FugueVersion = in.fugueVersion
	}
	rec.ProtocolVersion = json.RawMessage(strconv.Quote(in.protocol))
	rec.StartedAt = clk.Now().UTC().Format(time.RFC3339)
	passed, err := askSteps(stdin, stdout, in.id, in.check)
	if err != nil {
		return err
	}
	rec.Result = resultFail
	if passed {
		rec.Result = resultPass
	}
	rec.FinishedAt = clk.Now().UTC().Format(time.RFC3339)
	if !passed && strings.TrimSpace(in.notes) == "" {
		return fmt.Errorf("%w: a failed check needs -notes saying what failed", ErrVerifyLive)
	}
	path, err := writeRecord(in.dir, &rec, clk.Now().UTC().Format(time.DateOnly))
	if err != nil {
		return err
	}
	return say(stdout, "wrote %s (result %s, revision %s); commit it and name it in the Evidence column of %s", path, rec.Result, rec.Revision[:12], registryFile)
}

// verifyLiveInput is the validated command line of verify-live.
type verifyLiveInput struct {
	dir          string
	id           string
	check        *liveCheck
	notes        string
	fugueVersion string
	protocol     string
	measurements map[string]any
}

func parseVerifyLive(args []string) (*verifyLiveInput, error) {
	fs := newFlags("verify-live")
	dir := fs.String("dir", ".", "repository directory")
	id := fs.String("id", "", "verification ID, for example VR-E04")
	notes := fs.String("notes", "", "owner notes; no secret, prompt, model output or account label")
	fugueVersion := fs.String("fugue-version", "", "version of the binary under test (default: git describe of HEAD)")
	protocol := fs.String("protocol-version", "v1", "Host API version under test")
	var measures multiFlag
	fs.Var(&measures, "measure", "a measurement as name=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUsage, err)
	}
	check, ok := liveChecks()[*id]
	if !ok || fs.NArg() != 0 {
		return nil, fmt.Errorf("%w: verify-live -id must be one of %s", ErrUsage, strings.Join(sortedKeys(liveChecks()), ", "))
	}
	measurements, err := parseMeasurements(measures, check.measurements)
	if err != nil {
		return nil, err
	}
	if leak := secretFindings(*notes + " " + strings.Join(measures, " ")); len(leak) > 0 {
		return nil, fmt.Errorf("%w: notes or measurements contain %s; a record holds no secret", ErrVerifyLive, strings.Join(leak, ", "))
	}
	// The command line is checked before the machine, so a usage error reads
	// the same on every OS.
	if len(check.oses) > 0 && !slices.Contains(check.oses, runtime.GOOS) {
		return nil, fmt.Errorf("%w: %s runs on %s only, this machine is %s", ErrVerifyLive, *id, strings.Join(check.oses, " and "), runtime.GOOS)
	}
	return &verifyLiveInput{
		dir: *dir, id: *id, check: check, notes: *notes, fugueVersion: *fugueVersion,
		protocol: *protocol, measurements: measurements,
	}, nil
}

func sortedKeys(m map[string]*liveCheck) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// parseMeasurements turns name=value flags into the measurements object;
// numeric values stay numbers. Every name the check requires must be given.
func parseMeasurements(flags, required []string) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range flags {
		m := measurementFlag.FindStringSubmatch(f)
		if m == nil {
			return nil, fmt.Errorf("%w: -measure %q is not name=value", ErrUsage, f)
		}
		if n, err := strconv.ParseFloat(m[2], 64); err == nil {
			out[m[1]] = n
		} else {
			out[m[1]] = m[2]
		}
	}
	for _, name := range required {
		if _, ok := out[name]; !ok {
			return nil, fmt.Errorf("%w: this check needs -measure %s=<value>", ErrUsage, name)
		}
	}
	return out, nil
}

// newRecord fills the fields that come from the repository and the machine.
func newRecord(ctx context.Context, dir, id string, check *liveCheck, measurements map[string]any) (liveRecord, error) {
	if _, err := capture(ctx, dir, "git", "diff", "--quiet", "HEAD"); err != nil {
		return liveRecord{}, fmt.Errorf("%w: tracked files differ from HEAD; commit or stash so the record names the code you ran", ErrVerifyLive)
	}
	revision, err := resolveCommit(ctx, dir, "HEAD")
	if err != nil {
		return liveRecord{}, err
	}
	version, err := capture(ctx, dir, "git", "describe", "--tags", "--always", revision)
	if err != nil {
		return liveRecord{}, fmt.Errorf("%w: describe HEAD: %w", ErrVerifyLive, err)
	}
	schema, err := schemaVersion(ctx, dir, revision)
	if err != nil {
		return liveRecord{}, err
	}
	return liveRecord{
		ID: id, Check: check.name, Revision: revision, FugueVersion: strings.TrimSpace(version),
		SchemaVersion: json.RawMessage(strconv.Itoa(schema)),
		OS:            runtime.GOOS, Arch: runtime.GOARCH,
		Measurements: measurements, Covers: check.covers,
	}, nil
}

var migrationName = regexp.MustCompile(`^(\d{4})_.*\.sql$`)

// schemaVersion is the highest embedded migration at revision: the schema
// version of the fugue.db that build creates.
func schemaVersion(ctx context.Context, dir, revision string) (int, error) {
	out, err := capture(ctx, dir, "git", "ls-tree", "--name-only", revision, "internal/storage/migrations/")
	if err != nil {
		return 0, fmt.Errorf("%w: list migrations: %w", ErrVerifyLive, err)
	}
	version := 0
	for line := range strings.SplitSeq(out, "\n") {
		if m := migrationName.FindStringSubmatch(filepath.Base(strings.TrimSpace(line))); m != nil {
			n, convErr := strconv.Atoi(m[1])
			if convErr != nil {
				return 0, fmt.Errorf("%w: migration number %q: %w", ErrVerifyLive, m[1], convErr)
			}
			version = max(version, n)
		}
	}
	if version == 0 {
		return 0, fmt.Errorf("%w: no migrations found at %s", ErrVerifyLive, revision)
	}
	return version, nil
}

// askSteps prints the checklist for this OS and reads one y or n per step. It
// reports whether every step was confirmed; an unreadable answer fails closed.
func askSteps(stdin io.Reader, stdout io.Writer, id string, check *liveCheck) (bool, error) {
	if err := say(stdout, "%s (%s): confirm each step with y, or n if it did not hold.", id, check.name); err != nil {
		return false, err
	}
	in := bufio.NewScanner(stdin)
	passed := true
	for _, step := range check.steps {
		if len(step.oses) > 0 && !slices.Contains(step.oses, runtime.GOOS) {
			continue
		}
		if err := say(stdout, "- %s [y/N]", step.text); err != nil {
			return false, err
		}
		if !in.Scan() {
			return false, fmt.Errorf("%w: no answer for %q: run it in a terminal", ErrVerifyLive, step.text)
		}
		if answer := strings.ToLower(strings.TrimSpace(in.Text())); answer != "y" && answer != "yes" {
			passed = false
		}
	}
	return passed, nil
}

// writeRecord stores rec at quality/records/<ID>/<date>-<os>.json and refuses
// to replace a record: a second run of the same day gets a decision, not an
// overwrite.
func writeRecord(dir string, rec *liveRecord, date string) (string, error) {
	rel := filepath.ToSlash(filepath.Join("quality", "records", rec.ID, date+"-"+rec.OS+".json"))
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%w: encode record: %w", ErrVerifyLive, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("%w: open %s: %w", ErrVerifyLive, dir, err)
	}
	werr := writeNewFile(root, filepath.FromSlash(rel), append(raw, '\n'))
	if cerr := root.Close(); cerr != nil {
		werr = errors.Join(werr, fmt.Errorf("close %s: %w", dir, cerr))
	}
	if werr != nil {
		return "", fmt.Errorf("%w: %s: %w (delete an existing record to run again the same day)", ErrVerifyLive, rel, werr)
	}
	return rel, nil
}

func writeNewFile(root *os.Root, rel string, data []byte) error {
	if err := root.MkdirAll(filepath.Dir(rel), 0o750); err != nil {
		return fmt.Errorf("make directory: %w", err)
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	_, writeErr := f.Write(data)
	if closeErr := f.Close(); writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	return nil
}
