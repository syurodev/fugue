package app

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func closeHostAdvanceClock(t *testing.T, hp *HostProcess, clk *clock.Fake) {
	t.Helper()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		testutil.Spin(t, "close host", func() bool {
			select {
			case <-done:
				return true
			default:
				clk.Advance(time.Millisecond)
				return false
			}
		})
	})
	defer func() {
		close(done)
		wg.Wait()
	}()
	if err := hp.Close(); err != nil {
		t.Errorf("close host: %v", err)
	}
}

func startRecoveryHost(t *testing.T, dataDir, fugueHome string, clk *clock.Fake) *HostProcess {
	t.Helper()
	exe := mustExe(t)
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir:      dataDir,
		WorkspaceEnv: config.MapEnv{config.EnvFugueHome: fugueHome},
		Config:       testHostConfig(),
		Clock:        clk,
		IDs:          ids.NewUUIDv7(clk, ids.NewDeterministic("f116d-rec")),
		Transport:    newScriptedTransport(),
		VendorBinary: exe,
		Bridge:       VendorBridge{Binary: exe, Socket: filepath.Join(t.TempDir(), "host.sock")},
		TeamAdmitter: admitNothing{},
	})
	if err != nil {
		t.Fatalf("StartHost: %v", err)
	}
	return hp
}

func appendTeamEventsSeq(t *testing.T, store *vendorstore.Store, teamID string, seq int64, evs []vendorstore.AppendTeamEventInput) {
	t.Helper()
	ctx := t.Context()
	var err error
	for i := range evs {
		evCopy := evs[i]
		if seq, err = store.AppendTeamEvent(ctx, teamID, seq, &evCopy); err != nil {
			t.Fatalf("AppendTeamEvent(%s, %s): %v", teamID, evs[i].EventType, err)
		}
	}
}

func createTestRepoWorkspace(t *testing.T, hp *HostProcess) (wsID, canonRoot string) {
	t.Helper()
	ctx := t.Context()
	ws, err := hp.Runtime().CreateWorkspace(ctx, testAppPrincipal, teamTestWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	initRepo(t, repoDir)
	if _, cfgErr := hp.Runtime().UpdateWorkspaceConfig(ctx, testAppPrincipal, ws.ID, teamWorkspaceTOMLAt(t, "", repoDir), ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	canonRoot, err = hp.rt.workspaces.domain.SelectSessionCwd(ctx, ws.ID, teamTestRootName)
	if err != nil {
		t.Fatal(err)
	}
	return ws.ID, canonRoot
}
