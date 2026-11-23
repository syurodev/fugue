package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// runReleaseEvidence checks the release gate for the RC and, only when it is
// met, writes release-evidence.md (verification §7 step 3).
func runReleaseEvidence(ctx context.Context, clk clock.Clock, args []string, stdout io.Writer) error {
	fs := newFlags("release-evidence")
	dir := fs.String("dir", ".", "repository directory")
	rc := fs.String("rc", "HEAD", "release candidate commit; must be the checked-out commit")
	out := fs.String("out", "release-evidence.md", "file to write")
	var runs multiFlag
	fs.Var(&runs, "ci-run", "URL of a CI run on the RC (repeatable); recorded in the file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: release-evidence expects flags only", ErrUsage)
	}
	for _, r := range runs {
		if u, err := url.Parse(r); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%w: -ci-run %q is not an https URL", ErrUsage, r)
		}
	}
	report, err := checkRelease(ctx, *dir, *rc)
	if err != nil {
		return err
	}
	if len(report.Problems) > 0 {
		return fmt.Errorf("%w: %d blocker(s) for RC %s:\n- %s", ErrRelease, len(report.Problems), report.RC, strings.Join(report.Problems, "\n- "))
	}
	if err := os.WriteFile(filepath.Join(*dir, *out), []byte(renderEvidence(report, runs, clk.Now())), 0o600); err != nil {
		return fmt.Errorf("%w: write %s: %w", ErrRelease, *out, err)
	}
	return say(stdout, "wrote %s for RC %s: %d release rows, %d live records", *out, report.RC, len(report.Rows), len(report.Live))
}

// renderEvidence is the release-evidence.md that ships with the tag: the
// release rows of the registry, the live records behind them, the RC commit
// and the CI runs. Only a report without problems is rendered.
func renderEvidence(r *releaseReport, runs []string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Release evidence\n\nRC commit: `%s`\n\nGenerated: %s\n\n", r.RC, now.UTC().Format(time.RFC3339))
	b.WriteString("## CI runs\n\n")
	if len(runs) == 0 {
		b.WriteString("None recorded.\n")
	}
	for _, run := range runs {
		fmt.Fprintf(&b, "- %s\n", run)
	}
	b.WriteString("\n## Registry (gate `release`)\n\n| ID | Source | Tier | Evidence | Status |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | `%s` |\n", row.ID, row.Source, row.Tier, row.Evidence, row.Status)
	}
	b.WriteString("\n## Live records\n\n| ID | Check | Record | Result | OS/arch | Revision | Fugue version | Covers |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range r.Live {
		e := &r.Live[i]
		rec := &e.Record
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s/%s | `%s` | %s | `%s` |\n",
			rec.ID, rec.Check, e.Path, rec.Result, rec.OS, rec.Arch, rec.Revision, rec.FugueVersion, strings.Join(rec.Covers, "`, `"))
	}
	return b.String()
}
