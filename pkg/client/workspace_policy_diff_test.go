package client

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDiffRawTOMLPolicyDetectsPolicyChangesAndIgnoresNonPolicy(t *testing.T) {
	t.Parallel()

	oldTOML := []byte(`
[policy]
approval_mode = "auto"
shell_allow = ["git status"]

[teams]
idle_detach_seconds = 180
`)
	newTOML := []byte(`
[policy]
approval_mode = "never"
shell_allow = ["git status", "go test"]

[teams]
idle_detach_seconds = 300
`)

	got, err := DiffRawTOMLPolicy(oldTOML, newTOML)
	if err != nil {
		t.Fatalf("DiffRawTOMLPolicy: %v", err)
	}
	want := []PolicyDiffEntry{
		{Key: "policy.approval_mode", OldValue: "auto", NewValue: "never"},
		{Key: "policy.shell_allow", OldValue: "[git status]", NewValue: "[git status, go test]"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("DiffRawTOMLPolicy mismatch (-want +got):\n%s", diff)
	}
}

func TestDiffRawTOMLPolicyReturnsNilForNonPolicyOnly(t *testing.T) {
	t.Parallel()

	oldTOML := []byte("[teams]\nidle_detach_seconds = 180\n")
	newTOML := []byte("[teams]\nidle_detach_seconds = 240\n")

	got, err := DiffRawTOMLPolicy(oldTOML, newTOML)
	if err != nil {
		t.Fatalf("DiffRawTOMLPolicy: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no policy diff", got)
	}
}

func TestDiffRawTOMLPolicyErrorsOnInvalidTOML(t *testing.T) {
	t.Parallel()

	if _, err := DiffRawTOMLPolicy([]byte(""), []byte("[policy\nbroken")); err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}
