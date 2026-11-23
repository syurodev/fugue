//go:build drill

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/testutil"
)

// The cross-build drill of A-E08 (verification section 3, "Drill"): two real
// `fugue` binaries, the old one built from a base revision and the new one from
// HEAD, run `fugue serve` on one data directory. A shell script named claude
// stands in for the vendor CLI and replays the stream-json init event, so the
// drill needs nothing from either tree's test code and works for any base that
// has `fugue serve`. It runs only in the nightly job (`task drill`) because it
// builds an old tree, which a PR run must not pay for.

const (
	drillBuildTimeout   = 15 * time.Minute
	drillSchemaPaths    = "internal/storage/migrations proto"
	drillMigrationsPath = "internal/storage/migrations"
)

func TestDrillAcrossBuilds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), drillBuildTimeout)
	defer cancel()
	root := runGit(t, ".", "rev-parse", "--show-toplevel")
	base := drillBaseRevision(t, root)
	t.Logf("drill base %s, head %s", base, runGit(t, root, "rev-parse", "HEAD"))

	vendorEnv := drillFakeVendorEnv(t)
	oldBuild := hostBuild{exe: drillBuildFugue(t, ctx, drillExportRevision(t, ctx, root, base)), args: []string{testServe}, env: vendorEnv}
	newBuild := hostBuild{exe: drillBuildFugue(t, ctx, root), args: []string{testServe}, env: vendorEnv}
	oldMigrations := drillMigrationCount(t, root, base)
	newMigrations, err := storage.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	t.Logf("schema versions: base %d, head %d", oldMigrations, len(newMigrations))

	dataDir, fugueHome := crashTestDirsUnseeded(t)
	// Upgrade: the old build is killed mid-run and the new build resumes.
	sessionID := drillKillMidRun(t, oldBuild, dataDir, fugueHome, true)
	drillResumeAndAssertRecovered(t, newBuild, dataDir, fugueHome, sessionID)

	// Downgrade: the old build must not append to what the new build wrote. It
	// can only tell when the schema moved; a proto-only change is the
	// handshake's job and leaves the schema the old build accepts.
	if len(newMigrations) > oldMigrations {
		drillAssertRefusesToAppend(t, oldBuild, dataDir, fugueHome, string(errcode.FailedPrecondition))
		// The history the new build recovered is untouched: one turn, still unknown.
		assertTurnOutcomeUnknown(t, dataDir, sessionID)
		return
	}
	t.Log("schema unchanged between base and head: the synthetic-schema drill covers downgrade refusal")
}

// drillFakeVendorEnv puts a fake claude first on PATH.
func drillFakeVendorEnv(t *testing.T) []string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo '` + fakeE2EVendorVersion + ` (Claude Code)'; exit 0; fi` + "\n" +
		"printf '%s\\n' '" + strings.TrimSpace(fakeE2EInitLine) + "'\n" +
		"cat >/dev/null\n"
	testutil.WriteExecutable(t, filepath.Join(bin, "claude"), []byte(script))
	// Only the fake vendor and the system tools the host runs (git, sh): the
	// environment is read in internal/config alone.
	path := []string{bin, "/usr/local/bin", "/usr/bin", "/bin"}
	if git, err := exec.LookPath("git"); err == nil {
		path = append(path, filepath.Dir(git))
	}
	return []string{"PATH=" + strings.Join(path, string(os.PathListSeparator))}
}

// drillBaseRevision picks the older build: the merge-base with origin/main when
// the branch changed migrations or proto, else the parent of the last commit
// that did, so a run on main still exercises the previous schema generation.
func drillBaseRevision(t *testing.T, root string) string {
	t.Helper()
	paths := strings.Fields(drillSchemaPaths)
	if mb, ok := drillGit(t, root, "merge-base", "HEAD", "origin/main"); ok {
		diff := append([]string{"diff", "--quiet", mb, "HEAD", "--"}, paths...)
		if _, same := drillGit(t, root, diff...); !same {
			return mb
		}
	}
	last := runGit(t, root, append([]string{"log", "-1", "--format=%H", "--"}, paths...)...)
	if last == "" {
		t.Fatal("no commit changed migrations or proto, so there is no older schema generation to build")
	}
	return runGit(t, root, "rev-parse", last+"^")
}

// drillGit reports whether the git command succeeded; unlike runGit a failure
// is an answer, not a test failure.
func drillGit(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git")
	cmd.Args = append([]string{"git"}, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil
}

func drillMigrationCount(t *testing.T, root, rev string) int {
	t.Helper()
	listing := runGit(t, root, "ls-tree", "--name-only", rev, drillMigrationsPath+"/")
	count := 0
	for _, name := range strings.Fields(listing) {
		if strings.HasSuffix(name, ".sql") {
			count++
		}
	}
	return count
}

// drillExportRevision exports rev outside the checkout, leaving the working
// tree and its worktrees untouched, and returns the directory.
func drillExportRevision(t *testing.T, ctx context.Context, root, rev string) string {
	t.Helper()
	dir := t.TempDir()
	tarball := filepath.Join(dir, "base.tar")
	drillRun(t, ctx, root, "git", "archive", "--format=tar", "-o", tarball, rev)
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	drillRun(t, ctx, dir, "tar", "-xf", tarball, "-C", src)
	return src
}

// drillBuildFugue compiles cmd/fugue of the tree at src into a fresh directory.
func drillBuildFugue(t *testing.T, ctx context.Context, src string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "fugue")
	drillRun(t, ctx, src, "go", "build", "-o", exe, "./cmd/fugue")
	return exe
}

func drillRun(t *testing.T, ctx context.Context, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, name)
	cmd.Args = append([]string{name}, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
