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

var errHeavyStreamDraining = errors.New("host draining")

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
	lease, err := c.AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("AcquireHeavySlot: %v", err)
	}
	lease.Release()
	lease.Release() // idempotent close
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

func TestClientHeavyLeaseWaitDetectsServerCloseAndError(t *testing.T) {
	t.Parallel()
	closeEOF := make(chan struct{})
	eofHost := &fakeHandler{
		heavy: func(_ context.Context, _ *connect.Request[hostv1.AcquireHeavySlotRequest], stream *connect.ServerStream[hostv1.AcquireHeavySlotResponse]) error {
			if err := stream.Send(&hostv1.AcquireHeavySlotResponse{Acquired: true}); err != nil {
				return fmt.Errorf("send grant: %w", err)
			}
			<-closeEOF
			return nil
		},
	}
	eofLease, err := dialApp(t, serveHost(t, eofHost)).AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("AcquireHeavySlot eof: %v", err)
	}
	close(closeEOF)
	if waitErr := eofLease.Wait(); !errors.Is(waitErr, client.ErrDisconnected) {
		t.Fatalf("Wait on server EOF = %v, want ErrDisconnected", waitErr)
	}

	closeErr := make(chan struct{})
	errHost := &fakeHandler{
		heavy: func(_ context.Context, _ *connect.Request[hostv1.AcquireHeavySlotRequest], stream *connect.ServerStream[hostv1.AcquireHeavySlotResponse]) error {
			if sendErr := stream.Send(&hostv1.AcquireHeavySlotResponse{Acquired: true}); sendErr != nil {
				return fmt.Errorf("send grant: %w", sendErr)
			}
			<-closeErr
			return connect.NewError(connect.CodeUnavailable, errHeavyStreamDraining)
		},
	}
	errLease, err := dialApp(t, serveHost(t, errHost)).AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("AcquireHeavySlot err: %v", err)
	}
	close(closeErr)
	if waitErr := errLease.Wait(); connect.CodeOf(waitErr) != connect.CodeUnavailable {
		t.Fatalf("Wait on server error = %v, want Unavailable", waitErr)
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
	lease, err := c.AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("SessionClient.AcquireHeavySlot: %v", err)
	}
	lease.Release()
	<-released

	closeEOF := make(chan struct{})
	eofHost := &sessionHost{
		heavy: func(_ context.Context, _ *connect.Request[sessiontoolsv1.AcquireHeavySlotRequest], stream *connect.ServerStream[sessiontoolsv1.AcquireHeavySlotResponse]) error {
			if sendErr := stream.Send(&sessiontoolsv1.AcquireHeavySlotResponse{Acquired: true}); sendErr != nil {
				return fmt.Errorf("send session grant: %w", sendErr)
			}
			<-closeEOF
			return nil
		},
	}
	eofClient, err := client.DialSession(testCtx(t), serveSessionHost(t, eofHost), client.NewSessionToken(sessionTestValue))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eofClient.Close)
	eofLease, err := eofClient.AcquireHeavySlot(testCtx(t))
	if err != nil {
		t.Fatalf("SessionClient.AcquireHeavySlot eof: %v", err)
	}
	close(closeEOF)
	if waitErr := eofLease.Wait(); !errors.Is(waitErr, client.ErrDisconnected) {
		t.Fatalf("SessionClient Wait on server EOF = %v, want ErrDisconnected", waitErr)
	}

	unscripted, err := client.DialSession(testCtx(t), serveSessionHost(t, &sessionHost{}), client.NewSessionToken(sessionTestValue))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unscripted.Close)
	if _, err := unscripted.AcquireHeavySlot(testCtx(t)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("denied session err = %v, want PermissionDenied", err)
	}
}
