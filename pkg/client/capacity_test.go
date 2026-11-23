package client_test

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

var (
	errCapacityRefused = errors.New("the host already runs the maximum number of vendor sessions")
	errPlain           = errors.New("plain")
)

func capacityRefusal(t *testing.T, detail *hostv1.ErrorDetail) error {
	t.Helper()
	d, err := connect.NewErrorDetail(detail)
	if err != nil {
		t.Fatal(err)
	}
	cerr := connect.NewError(connect.CodeResourceExhausted, errCapacityRefused)
	cerr.AddDetail(d)
	return fmt.Errorf("start the session: %w", cerr)
}

// TestVendorCapacityOfReadsTheTypedCounts pins the decode: the counts ride the
// error detail, a wrapped refusal still reads, and any other code reports none.
func TestVendorCapacityOfReadsTheTypedCounts(t *testing.T) {
	t.Parallel()
	got, ok := client.VendorCapacityOf(capacityRefusal(t, &hostv1.ErrorDetail{
		Code: "vendor.capacity", RunningSessions: 8, SessionLimit: 8, SessionLimitScope: "machine",
	}))
	if !ok || got != (client.Capacity{Running: 8, Limit: 8, Scope: client.CapacityScopeMachine}) {
		t.Fatalf("capacity = %+v, %v; want 8 of 8 machine", got, ok)
	}
	if _, ok := client.VendorCapacityOf(capacityRefusal(t, &hostv1.ErrorDetail{Code: "vendor.vault_locked", RunningSessions: 1})); ok {
		t.Fatal("another code read as a capacity refusal")
	}
	if _, ok := client.VendorCapacityOf(errPlain); ok {
		t.Fatal("an error with no detail read as a capacity refusal")
	}
}
