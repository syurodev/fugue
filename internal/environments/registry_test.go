package environments

import (
	"fmt"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestBuiltinDefaults(t *testing.T) {
	t.Parallel()
	reg := New()

	if reg.RequiresConfirmation(TagDev) {
		t.Errorf("dev requires_confirmation = true, want false")
	}
	if !reg.RequiresConfirmation(TagStg) {
		t.Errorf("stg requires_confirmation = false, want true")
	}
	if !reg.RequiresConfirmation(TagProd) {
		t.Errorf("prod requires_confirmation = false, want true")
	}
	if reg.PolicyRevision(TagDev) == 0 {
		t.Errorf("dev policy revision is 0, want > 0")
	}
	if reg.PolicyRevision(TagStg) == 0 {
		t.Errorf("stg policy revision is 0, want > 0")
	}
	if reg.PolicyRevision(TagProd) == 0 {
		t.Errorf("prod policy revision is 0, want > 0")
	}
}

func TestProdIsLocked(t *testing.T) {
	t.Parallel()
	reg := New()

	err := reg.Set(TagProd, false)
	if err == nil {
		t.Fatal("expected error modifying locked prod tag, got nil")
	}
	if code := errcode.CodeOf(err); code != errcode.PermissionDenied {
		t.Errorf("errcode = %v, want PermissionDenied", code)
	}
	if !reg.RequiresConfirmation(TagProd) {
		t.Errorf("prod confirmation policy changed after rejected Set")
	}

	err = reg.Delete(TagProd)
	if err == nil {
		t.Fatal("expected error deleting locked prod tag, got nil")
	}
	if code := errcode.CodeOf(err); code != errcode.PermissionDenied {
		t.Errorf("errcode = %v, want PermissionDenied", code)
	}
	if !reg.RequiresConfirmation(TagProd) {
		t.Errorf("prod confirmation policy changed after rejected Delete")
	}
}

func TestRequiresConfirmationFailClosed(t *testing.T) {
	t.Parallel()
	reg := New()

	if !reg.RequiresConfirmation("") {
		t.Errorf("RequiresConfirmation(\"\") = false, want true (fail closed)")
	}
	if !reg.RequiresConfirmation("unknown-tag") {
		t.Errorf("RequiresConfirmation(\"unknown-tag\") = false, want true (fail closed)")
	}
	if !reg.RequiresConfirmation("local") {
		t.Errorf("RequiresConfirmation(\"local\") = false, want true (fail closed)")
	}
	if !reg.RequiresConfirmation("test") {
		t.Errorf("RequiresConfirmation(\"test\") = false, want true (fail closed)")
	}

	if reg.PolicyRevision("") != 0 {
		t.Errorf("PolicyRevision(\"\") = %d, want 0", reg.PolicyRevision(""))
	}
	if reg.PolicyRevision("unknown-tag") != 0 {
		t.Errorf("PolicyRevision(\"unknown-tag\") = %d, want 0", reg.PolicyRevision("unknown-tag"))
	}
}

func assertTagPolicyState(t *testing.T, reg *Registry, tag string, wantConfirm bool, prevRev uint64) uint64 {
	t.Helper()
	if got := reg.RequiresConfirmation(tag); got != wantConfirm {
		t.Errorf("%s requires confirmation = %v, want %v", tag, got, wantConfirm)
	}
	rev := reg.PolicyRevision(tag)
	if prevRev > 0 && rev <= prevRev {
		t.Errorf("%s revision %d <= prev %d", tag, rev, prevRev)
	}
	return rev
}

func TestUserDefinedTagsAndRevisions(t *testing.T) {
	t.Parallel()
	reg := New()

	const userTag = "custom-sandbox"
	if err := reg.Set(userTag, false); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	rev1 := assertTagPolicyState(t, reg, userTag, false, 0)
	if rev1 == 0 {
		t.Fatalf("expected non-zero revision for new tag")
	}

	// Change policy
	if err := reg.Set(userTag, true); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	rev2 := assertTagPolicyState(t, reg, userTag, true, rev1)

	// Revert policy: monotonic counter must strictly increase
	if err := reg.Set(userTag, false); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	_ = assertTagPolicyState(t, reg, userTag, false, rev2)

	// Delete user tag
	if err := reg.Delete(userTag); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !reg.RequiresConfirmation(userTag) {
		t.Errorf("deleted tag requires confirmation = false, want true (fail closed)")
	}
	if reg.PolicyRevision(userTag) != 0 {
		t.Errorf("deleted tag PolicyRevision = %d, want 0", reg.PolicyRevision(userTag))
	}
}

func TestBuiltinDevPolicyRevisionAcrossReversions(t *testing.T) {
	t.Parallel()
	reg := New()

	rev0 := reg.PolicyRevision(TagDev)
	if rev0 == 0 {
		t.Fatalf("initial dev revision is 0")
	}

	if err := reg.Set(TagDev, true); err != nil {
		t.Fatalf("Set dev true failed: %v", err)
	}
	rev1 := reg.PolicyRevision(TagDev)
	if rev1 <= rev0 {
		t.Errorf("rev1 (%d) <= rev0 (%d)", rev1, rev0)
	}

	if err := reg.Set(TagDev, false); err != nil {
		t.Fatalf("Set dev false failed: %v", err)
	}
	rev2 := reg.PolicyRevision(TagDev)
	if rev2 <= rev1 {
		t.Errorf("rev2 (%d) <= rev1 (%d)", rev2, rev1)
	}

	// Deleting built-in dev tag is disallowed
	err := reg.Delete(TagDev)
	if err == nil {
		t.Fatal("expected error deleting built-in dev tag, got nil")
	}
	if code := errcode.CodeOf(err); code != errcode.PermissionDenied {
		t.Errorf("errcode = %v, want PermissionDenied", code)
	}
}

func TestValidationErrors(t *testing.T) {
	t.Parallel()
	reg := New()

	if err := reg.Set("", false); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Errorf("Set(\"\") error = %v, want InvalidArgument", err)
	}
	if err := reg.Delete(""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Errorf("Delete(\"\") error = %v, want InvalidArgument", err)
	}
	// Deleting non-existent user tag is a no-op
	if err := reg.Delete("non-existent"); err != nil {
		t.Errorf("Delete(\"non-existent\") error = %v, want nil", err)
	}
}

func runBuiltinReaders(reg *Registry, iterations int) {
	for range iterations {
		_ = reg.RequiresConfirmation(TagDev)
		_ = reg.RequiresConfirmation(TagProd)
		_ = reg.PolicyRevision(TagDev)
		_ = reg.PolicyRevision(TagProd)
	}
}

func runDynamicReaders(reg *Registry, iterations int, tag string) {
	for range iterations {
		_ = reg.RequiresConfirmation(tag)
		_ = reg.PolicyRevision(tag)
	}
}

func runDynamicWriters(reg *Registry, iterations, id int) error {
	tag := fmt.Sprintf("dynamic-%d", id%4)
	for i := range iterations {
		b := i%2 == 0
		if err := reg.Set(tag, b); err != nil {
			return err
		}
		if id == 0 {
			if err := reg.Set(TagDev, b); err != nil && errcode.CodeOf(err) != errcode.PermissionDenied {
				return err
			}
		}
	}
	return nil
}

func TestConcurrentRegistryAccess(t *testing.T) {
	t.Parallel()
	reg := New()

	const iterations = 200

	for w := range 4 {
		t.Run(fmt.Sprintf("builtin_reader_%d", w), func(t *testing.T) {
			t.Parallel()
			runBuiltinReaders(reg, iterations)
		})

		tag := fmt.Sprintf("dynamic-%d", w)
		t.Run(fmt.Sprintf("dynamic_reader_%d", w), func(t *testing.T) {
			t.Parallel()
			runDynamicReaders(reg, iterations, tag)
		})

		workerID := w
		t.Run(fmt.Sprintf("writer_%d", w), func(t *testing.T) {
			t.Parallel()
			if err := runDynamicWriters(reg, iterations, workerID); err != nil {
				t.Errorf("writer failed: %v", err)
			}
		})
	}
}
