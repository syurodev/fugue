package vault

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

var errEpochAction = errors.New("epoch action failed")

func TestUnlockedEpochAuthorization(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	epoch := s.Snapshot()
	if err := s.WithUnlocked(epoch, func() error { return errEpochAction }); !errors.Is(err, errEpochAction) {
		t.Fatal("action result was lost")
	}
	if err := s.WithUnlocked(epoch, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("nil action: %v", err)
	}
	for _, bad := range []Snapshot{{Incarnation: "different", Revision: epoch.Revision, State: StateUnlocked}, {Incarnation: epoch.Incarnation, Revision: epoch.Revision + 1, State: StateUnlocked}, {Incarnation: epoch.Incarnation, Revision: epoch.Revision, State: StateLocked}} {
		if err := s.WithUnlocked(bad, func() error { t.Error("stale epoch authorized action"); return nil }); !errors.Is(err, ErrLocked) {
			t.Fatal("invalid epoch accepted")
		}
	}
	s.Lock()
	if err := s.WithUnlocked(epoch, func() error { t.Error("locked action ran"); return nil }); !errors.Is(err, ErrLocked) {
		t.Fatal("locked epoch accepted")
	}
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	if err := s.WithUnlocked(epoch, func() error { t.Error("old epoch reused"); return nil }); !errors.Is(err, ErrLocked) {
		t.Fatal("lock/unlock revived old epoch")
	}
	if err := s.WithUnlocked(s.Snapshot(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
