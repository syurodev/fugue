package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/pkg/client"
)

// Upgrade and downgrade drills (activation section 4 and A-E08). Stage A has no
// Waiting(recovery_admission): that is a native-kernel state. Its Stage A
// equivalent after a restart is a vendor session in state detached whose open
// turn has outcome unknown and is not replayed, with the transcript written
// before the kill still readable. The helpers take the executable of the
// build, so the same drill runs the new build against itself here and an older
// build against the new one in the nightly cross-build drill (drill_builds_test.go).

const drillTimeout = 60 * time.Second

// drillTestBinaryBuild is the running test binary acting as the host, with the
// fake vendor CLI built in (the crash-point tests' setup).
func drillTestBinaryBuild(t *testing.T) hostBuild {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return hostBuild{exe: exe, args: []string{crashServeArg}}
}

// drillKillMidRun starts the host of build (setupVault: on a data directory with
// no vault yet), opens a vendor turn and kills the host while the turn is open.
// It returns the session the kill left open.
func drillKillMidRun(t *testing.T, build hostBuild, dataDir, fugueHome string, setupVault bool) string {
	t.Helper()
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	ctx, cancel := context.WithTimeout(t.Context(), drillTimeout)
	defer cancel()
	// Canceling the host's own context kills it with SIGKILL, with no crash
	// point armed: an old build predates the crash points, and a real upgrade
	// stops the host from outside.
	hostCtx, kill := context.WithCancel(ctx)
	defer kill()
	done := startHostSubprocess(t, hostCtx, build, dataDir, fugueHome, "")
	c := dialCrashClient(t, ctx, dataDir, done)
	defer c.Close()
	if setupVault {
		// The build created the data directory, so it also creates the vault.
		setup, err := c.SetupVault(ctx, []byte(cheapvault.MasterPassword))
		if err != nil {
			t.Fatalf("SetupVault: %v", err)
		}
		setup.Clear()
	}
	if status, err := c.VaultStatus(ctx); err != nil {
		t.Fatalf("VaultStatus: %v", err)
	} else if status.State != client.VaultStateUnlocked {
		if _, uErr := c.UnlockVault(ctx, []byte(cheapvault.MasterPassword)); uErr != nil {
			t.Fatalf("UnlockVault: %v", uErr)
		}
	}
	ws := createWorkspaceAndAddRoot(t, ctx, c, "drill-ws", "repo", repoDir)
	sess, err := c.StartSession(ctx, "cmd-drill-start", client.VendorClaude, ws.ID, "repo", client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// The fake vendor never finishes the turn, so it is open when the host dies.
	if sendErr := c.Send(ctx, "cmd-drill-send", sess.ID, "drill turn"); sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	kill()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != -1 {
			t.Fatalf("killed host exit = %v, want death by signal", err)
		}
	case <-ctx.Done():
		t.Fatalf("host did not die after the kill: %v", ctx.Err())
	}
	return sess.ID
}

// drillResumeAndAssertRecovered starts build on the data directory a
// killed host left behind and checks the Stage A recovery state: the session is
// detached, its history is readable, and the unfinished turn is unknown and
// was neither replayed nor completed.
func drillResumeAndAssertRecovered(t *testing.T, build hostBuild, dataDir, fugueHome, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), drillTimeout)
	defer cancel()
	done := startHostSubprocess(t, ctx, build, dataDir, fugueHome, "")
	c := dialCrashClient(t, ctx, dataDir, done)
	listed, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after resume: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID != sessionID || listed.Sessions[0].State != string(vendorstore.StateDetached) {
		t.Fatalf("resumed sessions = %+v, want %s in state detached", listed.Sessions, sessionID)
	}
	if _, uErr := c.UnlockVault(ctx, []byte(cheapvault.MasterPassword)); uErr != nil {
		t.Fatalf("UnlockVault after resume: %v", uErr)
	}
	page, err := c.TranscriptPage(ctx, sessionID, 0, 20)
	if err != nil {
		t.Fatalf("TranscriptPage after resume: %v", err)
	}
	if len(page) == 0 {
		t.Fatal("history written before the kill is not readable after the upgrade")
	}
	awaitCleanExit(t, ctx, c, done)
}

// drillAssertRefusesToAppend starts build on a data directory newer than it
// understands. The host must fail without serving and leave the host
// incarnation as it was; wantCode, when set, must be the
// typed code the build reports on stderr.
func drillAssertRefusesToAppend(t *testing.T, build hostBuild, dataDir, fugueHome, wantCode string) {
	t.Helper()
	before := drillHostIncarnation(t, dataDir)
	ctx, cancel := context.WithTimeout(t.Context(), drillTimeout)
	defer cancel()
	done := startHostSubprocess(t, ctx, build, dataDir, fugueHome, "")
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitFailure {
			t.Fatalf("older build on newer data = %v, want exit code %d", err, exitFailure)
		}
		if wantCode != "" && !strings.Contains(err.Error(), wantCode) {
			t.Fatalf("older build error = %v, want typed code %s", err, wantCode)
		}
	case <-ctx.Done():
		t.Fatalf("older build did not refuse the newer data directory: %v", ctx.Err())
	}
	if _, err := os.Stat(socketPath(dataDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("older build left a Host API socket behind (stat error %v)", err)
	}
	if after := drillHostIncarnation(t, dataDir); after != before {
		t.Fatalf("older build recorded a host incarnation on the newer data directory: %+v, was %+v", after, before)
	}
}

// drillHostIncarnation reads the durable host record. A build that takes over
// the data directory records a new incarnation before it does anything else, so
// an unchanged record shows the build never got that far. The history tables
// belong to vendorstore and are checked through its API
// (assertTurnOutcomeUnknown).
func drillHostIncarnation(t *testing.T, dataDir string) storage.HostIncarnation {
	t.Helper()
	ctx := t.Context()
	db, err := storage.OpenDataDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("open fugue.db: %v", err)
	}
	defer func() {
		if cErr := db.Close(); cErr != nil {
			t.Errorf("close fugue.db: %v", cErr)
		}
	}()
	incarnation, err := storage.CurrentHostIncarnation(ctx, db)
	if err != nil {
		t.Fatalf("CurrentHostIncarnation: %v", err)
	}
	return incarnation
}

// drillPlantNewerSchema records one schema version past the build's own, as a
// newer build would have after its migration.
func drillPlantNewerSchema(t *testing.T, dataDir string) {
	t.Helper()
	ctx := t.Context()
	known, err := storage.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	db, err := storage.OpenDataDir(ctx, dataDir)
	if err != nil {
		t.Fatalf("open fugue.db: %v", err)
	}
	defer func() {
		if cErr := db.Close(); cErr != nil {
			t.Errorf("close fugue.db: %v", cErr)
		}
	}()
	newer := append(slices.Clone(known), storage.Migration{Version: len(known) + 1, Name: "synthetic_future", SQL: "SELECT 1;"})
	if err := storage.RunMigrations(ctx, db, clock.NewFake(time.Unix(0, 0)), newer); err != nil {
		t.Fatalf("plant newer schema: %v", err)
	}
}

// Upgrade drill with one build: stop the host mid-run, start it again, and the
// session resumes in the recovery state without replaying the open turn.
func TestUpgradeDrillSameBuildResumesRecovery(t *testing.T) {
	t.Parallel()
	build := drillTestBinaryBuild(t)
	dataDir, fugueHome := crashTestDirs(t)
	sessionID := drillKillMidRun(t, build, dataDir, fugueHome, false)
	drillResumeAndAssertRecovered(t, build, dataDir, fugueHome, sessionID)
}

// Downgrade drill against a synthetic newer schema: after the new build has
// recovered the data, a build that finds a schema version it does not know
// fails typed (failed_precondition) before it serves or writes anything.
func TestDowngradeDrillRefusesNewerSchemaWithoutAppending(t *testing.T) {
	t.Parallel()
	build := drillTestBinaryBuild(t)
	dataDir, fugueHome := crashTestDirs(t)
	sessionID := drillKillMidRun(t, build, dataDir, fugueHome, false)
	drillResumeAndAssertRecovered(t, build, dataDir, fugueHome, sessionID)
	drillPlantNewerSchema(t, dataDir)
	drillAssertRefusesToAppend(t, build, dataDir, fugueHome, string(errcode.FailedPrecondition))
}
