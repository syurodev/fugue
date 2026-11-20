package hostapi

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
)

func TestHeavyCommandSemaphoreMatchesCoresQuarterAndSharesExclusive(t *testing.T) {
	t.Parallel()
	// 8 cores -> max(1, 8/4) = 2 concurrent heavy-command slots shared between
	// CLI (`HostService`) and vendor session (`SessionToolService`) callers.
	v := startVendorHostWith(t, true, func(deps *app.HostDeps, sockPath string) {
		deps.Random = ids.NewDeterministic(sessionTokenSeed)
		deps.HostResources = fixedHostResources{ramBytes: 16 << 30, cores: 8}
		deps.Bridge = app.VendorBridge{Binary: deps.VendorBinary, Socket: sockPath}
	})
	wsID := v.workspaceWithRoot(t, t.TempDir())
	v.readySession(t, "heavy-sess", wsID, "")
	tokens := sessionTokens(t, 1)

	assertUnattachedHeavyDenied(t, v.sockPath)
	cli1 := dialHeavyHostClient(t, v.sockPath, "cli-1")
	cli2 := dialHeavyHostClient(t, v.sockPath, "cli-2")
	sess := dialHeavySessionClient(t, v.sockPath, tokens[0])

	rel1, err := acquireHostHeavySlot(t.Context(), cli1)
	if err != nil {
		t.Fatalf("cli1 AcquireHeavySlot: %v", err)
	}
	defer rel1()
	relSess, err := acquireSessionHeavySlot(t.Context(), sess)
	if err != nil {
		t.Fatalf("sess AcquireHeavySlot: %v", err)
	}
	defer relSess()

	got3, err3 := acquireHeavyAsync(t.Context(), cli2)
	select {
	case rel2 := <-got3:
		rel2()
		t.Fatal("3rd heavy slot granted while 2 slots (cores/4) are held")
	case err := <-err3:
		t.Fatalf("cli2 AcquireHeavySlot unexpected error: %v", err)
	default:
	}
	relSess()
	select {
	case rel2 := <-got3:
		rel2()
	case err := <-err3:
		t.Fatalf("cli2 AcquireHeavySlot after release: %v", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func dialHeavyHostClient(t *testing.T, sockPath, name string) hostv1connect.HostServiceClient {
	t.Helper()
	cli, _ := newTestClient(t, sockPath)
	_, err := cli.Hello(t.Context(), connect.NewRequest(&hostv1.HelloRequest{
		ClientName: name, ClientKind: hostv1.ClientKind_CLIENT_KIND_CLI, SupportedApiVersions: []string{"v1"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

func dialHeavySessionClient(t *testing.T, sockPath, token string) sessiontoolsv1connect.SessionToolServiceClient {
	t.Helper()
	_, hc := newTestClient(t, sockPath)
	sess := sessiontoolsv1connect.NewSessionToolServiceClient(hc, "http://fugue")
	if _, err := sess.Attach(t.Context(), connect.NewRequest(&sessiontoolsv1.AttachRequest{SessionToken: token})); err != nil {
		t.Fatal(err)
	}
	return sess
}

type testHeavyStream[Res any] interface {
	Receive() bool
	Msg() *Res
	Err() error
	Close() error
}

func waitTestHeavyGrant[Res any](stream testHeavyStream[Res], acquired func(*Res) bool) (func(), error) {
	for stream.Receive() {
		if acquired(stream.Msg()) {
			var once sync.Once
			return func() { once.Do(func() { closeQuietly(stream) }) }, nil
		}
	}
	closeQuietly(stream)
	return nil, fmt.Errorf("heavy slot stream ended: %w", stream.Err())
}

func acquireHostHeavySlot(ctx context.Context, cli hostv1connect.HostServiceClient) (func(), error) {
	stream, err := cli.AcquireHeavySlot(ctx, connect.NewRequest(&hostv1.AcquireHeavySlotRequest{}))
	if err != nil {
		return nil, fmt.Errorf("acquire host heavy slot: %w", err)
	}
	return waitTestHeavyGrant(stream, func(m *hostv1.AcquireHeavySlotResponse) bool { return m.GetAcquired() })
}

func acquireSessionHeavySlot(ctx context.Context, sess sessiontoolsv1connect.SessionToolServiceClient) (func(), error) {
	stream, err := sess.AcquireHeavySlot(ctx, connect.NewRequest(&sessiontoolsv1.AcquireHeavySlotRequest{}))
	if err != nil {
		return nil, fmt.Errorf("acquire session heavy slot: %w", err)
	}
	return waitTestHeavyGrant(stream, func(m *sessiontoolsv1.AcquireHeavySlotResponse) bool { return m.GetAcquired() })
}

func assertUnattachedHeavyDenied(t *testing.T, sockPath string) {
	t.Helper()
	_, hc := newTestClient(t, sockPath)
	unattached := sessiontoolsv1connect.NewSessionToolServiceClient(hc, "http://fugue")
	stream, err := unattached.AcquireHeavySlot(t.Context(), connect.NewRequest(&sessiontoolsv1.AcquireHeavySlotRequest{}))
	if err != nil {
		return
	}
	if stream.Receive() {
		t.Fatal("unattached AcquireHeavySlot succeeded")
	}
	if stream.Err() == nil {
		t.Fatal("expected stream error for unattached AcquireHeavySlot")
	}
	closeQuietly(stream)
}

func acquireHeavyAsync(ctx context.Context, cli hostv1connect.HostServiceClient) (<-chan func(), <-chan error) {
	got := make(chan func(), 1)
	errs := make(chan error, 1)
	supervise.Go(ctx, "test.heavy-waiter", func(taskCtx context.Context) error {
		rel, err := acquireHostHeavySlot(taskCtx, cli)
		if err != nil {
			errs <- err
			return fmt.Errorf("acquire heavy slot: %w", err)
		}
		got <- rel
		return nil
	})
	return got, errs
}
