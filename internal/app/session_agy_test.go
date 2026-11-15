package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
)

func startAgyServiceTestHost(t *testing.T) (*HostProcess, *VendorService) {
	t.Helper()
	exe := mustExe(t)
	agyLink := filepath.Join(t.TempDir(), "agy")
	if err := os.Symlink(exe, agyLink); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(200, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir:      t.TempDir(),
		WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config:       testHostConfig(),
		Clock:        clk,
		IDs:          ids.NewUUIDv7(clk, ids.NewDeterministic("agy-service")),
		Transport:    newScriptedTransport(),
		VendorBinary: exe,
		AgyBinary:    agyLink,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	if _, setupErr := hp.Vault().Setup(t.Context(), redact.NewSecret("agy-service-pw")); setupErr != nil {
		t.Fatal(setupErr)
	}
	svc, err := hp.Runtime().VendorService()
	if err != nil {
		t.Fatal(err)
	}
	return hp, svc
}

func TestAgyServiceProbeStartSendCancelAndClose(t *testing.T) {
	t.Parallel()
	hp, svc := startAgyServiceTestHost(t)
	rt := hp.Runtime()

	probes, err := svc.Probe(t.Context(), testAppPrincipal, []string{VendorAGY})
	if err != nil || len(probes) != 1 || probes[0].Vendor != VendorAGY || !probes[0].VersionTested || probes[0].Version != fakeAgyVersion {
		t.Fatalf("probes = %+v, %v; want tested agy %s", probes, err, fakeAgyVersion)
	}

	rootDir := t.TempDir()
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, "agy-ws")
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte("name = \"agy-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(rootDir) + "\"\naccess = \"rw\"\n")
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, cfg, ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}

	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	sess, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{
		CommandID: "agy-start-1", Vendor: VendorAGY, WorkspaceID: ws.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, sess.ID, "ready")
	verifyAgyTurnAndCancel(t, svc, sub, sess.ID)

	if _, closeErr := svc.Close(t.Context(), testAppPrincipal, VendorStop{
		CommandID: "agy-close-1", SessionID: sess.ID,
	}); closeErr != nil {
		t.Fatal(closeErr)
	}
	awaitSessionState(t, sub, sess.ID, testStateClosed)
}

func verifyAgyTurnAndCancel(t *testing.T, svc *VendorService, sub *Subscription, sessionID string) {
	t.Helper()
	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "agy-send-1", SessionID: sessionID, Text: "ping",
	}); sendErr != nil {
		t.Fatal(sendErr)
	}
	awaitSessionState(t, sub, sessionID, "running")
	awaitSessionState(t, sub, sessionID, "ready")

	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{
		CommandID: "agy-send-2", SessionID: sessionID, Text: fakeCLIHold,
	}); sendErr != nil {
		t.Fatal(sendErr)
	}
	awaitSessionState(t, sub, sessionID, "running")
	if _, intErr := svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{
		CommandID: "agy-int-1", SessionID: sessionID,
	}); intErr != nil {
		t.Fatal(intErr)
	}
	awaitSessionState(t, sub, sessionID, "ready")
}
