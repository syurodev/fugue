package hostconn

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

const (
	testOldVersion = "1.2.0"
	testNewVersion = "1.3.0"
	testDevVersion = "dev"
)

func TestVersionAdviceHonorsOtherClients(t *testing.T) {
	t.Parallel()
	cases := []struct {
		app, host string
		counts    client.Counts
		want      string
	}{
		{testOldVersion, testNewVersion, client.Counts{App: 1, Total: 1}, "Fugue host v1.3.0 is newer; update the app"},
		{testNewVersion, testOldVersion, client.Counts{App: 1, Total: 1}, "Fugue host v1.2.0 is older; restart it to upgrade"},
		{testNewVersion, testOldVersion, client.Counts{App: 1, CLI: 1, Total: 2}, "Fugue host v1.2.0 is busy; upgrade when it is idle"},
		{testOldVersion, testOldVersion, client.Counts{App: 1, Total: 1}, ""},
		{testOldVersion, "unknown", client.Counts{App: 1, Total: 1}, "Cannot compare host and app versions; update the app"},
	}
	for _, tc := range cases {
		if got := versionAdvice(tc.app, tc.host, tc.counts); got != tc.want {
			t.Fatalf("%s / %s: %q, want %q", tc.app, tc.host, got, tc.want)
		}
	}
}

type versionHost struct {
	fakeHandler
	shutdowns atomic.Int32
}

func (h *versionHost) HostStatus(context.Context, *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
	return connect.NewResponse(&hostv1.HostStatusResponse{HostVersion: testOldVersion, Clients: &hostv1.ClientCounts{App: 1, Cli: 1, Total: 2}}), nil
}

func (h *versionHost) Shutdown(context.Context, *connect.Request[hostv1.ShutdownRequest]) (*connect.Response[hostv1.ShutdownResponse], error) {
	h.shutdowns.Add(1)
	return connect.NewResponse(&hostv1.ShutdownResponse{}), nil
}

func TestOlderBusyHostIsReportedWithoutShutdown(t *testing.T) {
	t.Parallel()
	h := &versionHost{}
	socket := serveFakeHost(t, h)
	testSetMode(t, socket, 0o600)
	dir := dataDirWithHost(t, socket)
	testSetMode(t, dir, 0o700)
	params := appParams()
	params.Version = testNewVersion
	updates := make(chan Update, 128)
	m := NewManager(&Config{DataDir: dir, Params: params, AutoStart: true, Updates: updates})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	m.Start(ctx)
	defer m.Stop()
	for {
		select {
		case u := <-updates:
			if u.State == StateIncompatible {
				if u.Reason != "Fugue host v1.2.0 is busy; upgrade when it is idle" {
					t.Fatalf("reason = %q", u.Reason)
				}
				if h.shutdowns.Load() != 0 {
					t.Fatal("busy host was shut down")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("missing version message")
		}
	}
}
