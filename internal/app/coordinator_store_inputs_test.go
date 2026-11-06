package app

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/execstore"
)

func TestAdapterPassesExpectedAdmissionRevisionThroughUnchanged(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, false)
	tr := adapterAccept()
	tr.ExpectedAdmissionRevision = 0
	_, err := NewCoordinatorStore(s).Append(t.Context(), tr)
	if !errors.Is(err, execstore.ErrStaleRevision) || errors.Is(err, coordinator.ErrCommitUnknown) {
		t.Fatalf("revision 0 must reach the store and be rejected as stale, got %v", err)
	}
	if _, found, readErr := s.LookupReceipt(t.Context(), adapterAcceptCommand); readErr != nil || found {
		t.Fatalf("a stale expectation must not commit: found=%t err=%v", found, readErr)
	}
}
