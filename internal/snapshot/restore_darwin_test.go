//go:build darwin

package snapshot

import (
	"context"
	"testing"
)

func TestDecomposedFilenamesRoundTrip(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "café.txt", "one\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "café.txt", "two\n")
	after := snap(t, repo, "cp1", SlotAfter)

	changes, err := repo.Diff(context.Background(), before.Tree, after.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeModified {
		t.Fatalf("changes = %+v, want one modification", changes)
	}
}
