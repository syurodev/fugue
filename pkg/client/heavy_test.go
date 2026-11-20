package client_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
)

func TestClientAcquireHeavySlotGrantAndErrors(t *testing.T) {
	t.Parallel()
	released := make(chan struct{}, 1)
	h := &fakeHandler{
		heavy: func(ctx context.Context, _ *connect.Request[hostv1.AcquireHeavySlotRequest], stream *connect.ServerStream[hostv1.AcquireHeavySlotResponse]) error {
			if err := stream.Send(&hostv1.AcquireHeavySlotResponse{Acquired: true}); err != nil {
				return fmt.Errorf("send grant: %w", err)
			}
			<-ctx.Done()
			released <- struct{}{}
			return nil
		},
	}
	c := dialApp(t, serveHost(t, h))
	rel, err := c.AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("AcquireHeavySlot: %v", err)
	}
	rel()
	rel() // idempotent close
	<-released

	empty := &fakeHandler{
		heavy: func(context.Context, *connect.Request[hostv1.AcquireHeavySlotRequest], *connect.ServerStream[hostv1.AcquireHeavySlotResponse]) error {
			return nil
		},
	}
	if _, err := dialApp(t, serveHost(t, empty)).AcquireHeavySlot(testCtx(t)); !errors.Is(err, client.ErrDisconnected) {
		t.Fatalf("empty stream err = %v, want ErrDisconnected", err)
	}

	denied := dialApp(t, serveHost(t, &fakeHandler{}))
	if _, err := denied.AcquireHeavySlot(testCtx(t)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("denied err = %v, want PermissionDenied", err)
	}
}

func TestSessionClientAcquireHeavySlotGrantAndError(t *testing.T) {
	t.Parallel()
	released := make(chan struct{}, 1)
	h := &sessionHost{
		heavy: func(ctx context.Context, _ *connect.Request[sessiontoolsv1.AcquireHeavySlotRequest], stream *connect.ServerStream[sessiontoolsv1.AcquireHeavySlotResponse]) error {
			if err := stream.Send(&sessiontoolsv1.AcquireHeavySlotResponse{Acquired: true}); err != nil {
				return fmt.Errorf("send session grant: %w", err)
			}
			<-ctx.Done()
			released <- struct{}{}
			return nil
		},
	}
	c, err := client.DialSession(testCtx(t), serveSessionHost(t, h), client.NewSessionToken(sessionTestValue))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	rel, err := c.AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("SessionClient.AcquireHeavySlot: %v", err)
	}
	rel()
	<-released

	unscripted, err := client.DialSession(testCtx(t), serveSessionHost(t, &sessionHost{}), client.NewSessionToken(sessionTestValue))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unscripted.Close)
	if _, err := unscripted.AcquireHeavySlot(testCtx(t)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("denied session err = %v, want PermissionDenied", err)
	}
}
