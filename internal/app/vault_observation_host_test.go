package app

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// scanDataDirForMarker fails when any regular file under dir holds the marker
// bytes: the database, its journal and every file the host wrote.
func scanDataDirForMarker(t *testing.T, dir string, marker []byte) {
	t.Helper()
	fsys := os.DirFS(dir)
	walkErr := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		raw, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		if bytes.Contains(raw, marker) {
			t.Errorf("plaintext marker found in %s", path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("scan data dir: %v", walkErr)
	}
}

// TestHeadlessHostLockBetweenConsumeStepsFailsClosedWithoutPlaintext is the
// headless half of S-E07: on a host composed without a bridge, a vault lock
// between an approval and the claim of its receipt, and between two protected
// writes, fails closed. The lock reaches the interaction owner and the store
// through the host's own composition, and no plaintext marker lands on disk.
func TestHeadlessHostLockBetweenConsumeStepsFailsClosedWithoutPlaintext(t *testing.T) {
	t.Parallel()
	f := newVaultLockFixture(t)
	dataDir := f.hp.store.DataDir()
	unlockCheapVault(t, f.hp)
	vs := f.hp.vendorStore
	marker := []byte("planted-headless-consume-marker-51c7")

	snap, snapErr := vs.Snapshot(t.Context(), "sess-vault-lock")
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	next, err := vs.Append(t.Context(), "sess-vault-lock", snap.LastSequence, &vendorstore.Event{Type: vendorstore.UsageObserved, Payload: marker})
	if err != nil {
		t.Fatalf("protected append while unlocked: %v", err)
	}
	binding := interaction.Binding{
		SessionID: "sess-vault-lock", WorkspaceID: testWorkspaceID, InvocationID: "inv-se07",
		AttemptID: "att-se07", CheckGeneration: 1, ToolName: toolDeploy,
	}
	owner := f.rt.interactions.owner
	ticket, _, ensureErr := owner.Ensure(&interaction.Spec{
		OperationID: "op-se07", Kind: interaction.KindConfirm, Summary: "Deploy",
		Offers: []interaction.Outcome{interaction.OutcomeAllowedOnce}, Binding: binding,
	})
	if ensureErr != nil {
		t.Fatal(ensureErr)
	}
	answered, answerErr := owner.Answer(&interaction.Decision{
		Incarnation: owner.Incarnation(), RequestID: ticket.RequestID, Outcome: interaction.OutcomeAllowedOnce,
	})
	if answerErr != nil {
		t.Fatalf("answer while unlocked: %v", answerErr)
	}

	f.hp.Vault().Lock()

	if _, claimErr := owner.ClaimReceipt(answered.ReceiptID, &binding); errcode.CodeOf(claimErr) != errcode.FailedPrecondition {
		t.Fatalf("ClaimReceipt after Lock = %v, want FailedPrecondition", claimErr)
	}
	if _, appendErr := vs.Append(t.Context(), "sess-vault-lock", next, &vendorstore.Event{Type: vendorstore.UsageObserved, Payload: marker}); !errors.Is(appendErr, vault.ErrLocked) {
		t.Fatalf("protected append after Lock = %v, want vault.ErrLocked", appendErr)
	}
	testutil.Spin(t, "owner marked locked", func() bool { return len(owner.Pending()) == 0 })
	scanDataDirForMarker(t, dataDir, marker)
}

// TestHostRecoveryOfProtectedWorkWaitsForTheUnlock is the unlock-before-
// recovery half of S-E07: a restarted host recovers nothing that needs the
// vault while it is locked (no card, no mailbox notice, the unknown task is
// untouched), and the unlock that follows admits that recovery exactly once.
func TestHostRecoveryOfProtectedWorkWaitsForTheUnlock(t *testing.T) {
	t.Parallel()
	dataDir := cheapVaultDataDir(t)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC))
	setupRebuildCardsHost1(t, dataDir, fugueHome, clk)

	clk.Advance(time.Minute)
	hp2 := startRecoveryHost(t, dataDir, fugueHome, clk)
	defer closeHostAdvanceClock(t, hp2, clk)

	views, _, err := hp2.rt.interactions.ListPending(t.Context(), testAppPrincipal)
	if err != nil || len(views) != 0 {
		t.Fatalf("cards while the vault is locked = (%d, %v), want 0", len(views), err)
	}
	if pending, pendErr := hp2.vendorStore.PendingMailbox(t.Context()); pendErr != nil || len(pending) != 0 {
		t.Fatalf("mailbox while the vault is locked = (%d, %v), want 0", len(pending), pendErr)
	}
	if recErr := hp2.rt.teamTasks.RecoverOnUnlock(t.Context()); recErr == nil {
		if after, _, listErr := hp2.rt.interactions.ListPending(t.Context(), testAppPrincipal); listErr != nil || len(after) != 0 {
			t.Fatalf("RecoverOnUnlock on a locked vault left %d cards (%v)", len(after), listErr)
		}
	}

	unlockCheapVault(t, hp2)
	qTicketID := assertRebuiltCardsPresent(t, hp2)
	assertSecondUnlockIdempotent(t, hp2, qTicketID)
}
