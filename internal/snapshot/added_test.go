package snapshot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const forgedFile = "forged.txt"

type addedRecord struct {
	Path string
	Line int
	Text string
}

func collectAdded(t *testing.T, repo *Repo, from, to string, maxBytes int64) ([]addedRecord, AddedScan) {
	t.Helper()
	var got []addedRecord
	scan, err := repo.AddedLines(context.Background(), from, to, maxBytes, func(l *AddedLine) {
		got = append(got, addedRecord{Path: l.Path, Line: l.Number, Text: string(l.Text)})
	})
	if err != nil {
		t.Fatalf("AddedLines: %v", err)
	}
	return got, scan
}

// Added lines carry their path and new-file line number; removed lines and the
// unchanged lines around a change never appear.
func TestAddedLinesYieldsOnlyAddedLinesWithPathAndNumber(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "keep.txt", "a\nb\nc\n")
	write(t, repo, "gone.txt", "removed secret-looking line\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "keep.txt", "a\nB\nc\nd\n")
	write(t, repo, "new file.txt", "x\ny\n")
	write(t, repo, "gone.txt", "")
	after := snap(t, repo, "cp1", SlotAfter)

	got, scan := collectAdded(t, repo, before.Tree, after.Tree, 1<<20)
	want := []addedRecord{
		{"keep.txt", 2, "B"},
		{"keep.txt", 4, "d"},
		{"new file.txt", 1, "x"},
		{"new file.txt", 2, "y"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("added lines mismatch (-want +got):\n%s", diff)
	}
	if scan.Truncated || scan.Binary != 0 {
		t.Fatalf("scan = %+v, want complete and no binary", scan)
	}
}

// A line whose content looks like a diff header is still content: headers are
// only read before the first hunk of a file.
func TestAddedLinesContentCannotForgeDiffHeaders(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "base.txt", "x\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, forgedFile, "diff --git a b\n+++ other.txt\n@@ -1 +9 @@\nreal\n")
	after := snap(t, repo, "cp1", SlotAfter)

	got, _ := collectAdded(t, repo, before.Tree, after.Tree, 1<<20)
	want := []addedRecord{
		{forgedFile, 1, "diff --git a b"},
		{forgedFile, 2, "+++ other.txt"},
		{forgedFile, 3, "@@ -1 +9 @@"},
		{forgedFile, 4, "real"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("added lines mismatch (-want +got):\n%s", diff)
	}
}

func TestAddedLinesCountsBinaryFilesAndNeverReadsThem(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "base.txt", "x\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "blob.bin", "head\x00tail\n")
	after := snap(t, repo, "cp1", SlotAfter)

	got, scan := collectAdded(t, repo, before.Tree, after.Tree, 1<<20)
	if len(got) != 0 || scan.Binary != 1 {
		t.Fatalf("got %d lines and %d binary files, want 0 and 1", len(got), scan.Binary)
	}
}

// Past the byte cap the head is delivered and the scan says it is truncated.
func TestAddedLinesStopsAtTheByteCap(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "base.txt", "x\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "big.txt", strings.Repeat("0123456789\n", 100))
	after := snap(t, repo, "cp1", SlotAfter)

	got, scan := collectAdded(t, repo, before.Tree, after.Tree, 55)
	if len(got) != 5 || !scan.Truncated || scan.Bytes != 50 {
		t.Fatalf("got %d lines, bytes %d, truncated %v; want 5, 50, true", len(got), scan.Bytes, scan.Truncated)
	}
}

// One line longer than the read buffer is cut, flagged, and does not stop the
// lines after it.
func TestAddedLinesCutsAnOverlongLineAndContinues(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "base.txt", "x\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "min.js", strings.Repeat("a", maxAddedLine+500)+"\nafter\n")
	after := snap(t, repo, "cp1", SlotAfter)

	got, scan := collectAdded(t, repo, before.Tree, after.Tree, 1<<30)
	if len(got) != 2 || len(got[0].Text) != maxAddedLine-1 || got[1].Text != "after" || got[1].Line != 2 {
		t.Fatalf("overlong line handling wrong: %d lines", len(got))
	}
	if !scan.Truncated {
		t.Fatal("an overlong line must mark the scan truncated")
	}
}

func TestAddedLinesRejectsMissingTrees(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	if _, err := repo.AddedLines(context.Background(), "", "abc", 10, func(*AddedLine) {}); err == nil {
		t.Fatal("a missing tree must fail")
	}
}
