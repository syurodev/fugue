package client_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
)

// vendorCloseFake records the CloseSession RPC the client wrapper sends.
type vendorCloseFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	vendorsv1connect.UnimplementedVendorServiceHandler

	mu      sync.Mutex
	done    bool
	session []*vendorsv1.CloseSessionRequest
}

func (f *vendorCloseFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{}), nil
}

func (f *vendorCloseFake) CloseSession(_ context.Context, req *connect.Request[vendorsv1.CloseSessionRequest]) (*connect.Response[vendorsv1.CloseSessionResponse], error) {
	f.mu.Lock()
	f.session = append(f.session, req.Msg)
	done := f.done
	f.mu.Unlock()
	status := vendorsv1.OperationStatus_OPERATION_STATUS_PENDING
	if done {
		status = vendorsv1.OperationStatus_OPERATION_STATUS_DONE
	}
	return connect.NewResponse(&vendorsv1.CloseSessionResponse{Status: status}), nil
}

func (f *vendorCloseFake) taken() []*vendorsv1.CloseSessionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

// serveVendorCloseFake serves the fake and dials a client at it.
func serveVendorCloseFake(t *testing.T, fake *vendorCloseFake) *client.Client {
	t.Helper()
	return dialScriptedHost(t, "close.sock", func(mux *http.ServeMux) {
		mux.Handle(hostv1connect.NewHostServiceHandler(fake))
		mux.Handle(vendorsv1connect.NewVendorServiceHandler(fake))
	})
}

// TestClientCloseSessionSendsFieldsAndReportsDone pins the client wrapper:
// the close carries the command id and session verbatim, and Done reports
// the host's operation status, so a close still running on the host reads as
// not-done rather than as a failure.
func TestClientCloseSessionSendsFieldsAndReportsDone(t *testing.T) {
	t.Parallel()
	fake := &vendorCloseFake{done: true}
	c := serveVendorCloseFake(t, fake)
	ctx := testCtx(t)

	done, err := c.CloseSession(ctx, "cmd-close-1", "sess-7")
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("a DONE close reported not-done")
	}
	sent := fake.taken()
	if len(sent) != 1 || sent[0].GetCommandId() != "cmd-close-1" || sent[0].GetSessionId() != "sess-7" {
		t.Fatalf("close requests = %+v, want one with the sent fields", sent)
	}

	fake.mu.Lock()
	fake.done = false
	fake.mu.Unlock()
	done, err = c.CloseSession(ctx, "cmd-close-2", "sess-7")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("a still-running close reported done")
	}
}
