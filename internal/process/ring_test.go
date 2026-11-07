package process

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRingBufferKeepsTail64KiB(t *testing.T) {
	t.Parallel()

	ring := NewRingBuffer(DefaultStderrRingCapacity)

	if ring.Len() != 0 {
		t.Fatalf("initial Len = %d, want 0", ring.Len())
	}
	if tail := ring.Tail(); tail != nil {
		t.Fatalf("initial Tail = %v, want nil", tail)
	}

	// 1. Partial write < 64 KiB
	part1 := []byte("hello stderr\n")
	n, err := ring.Write(part1)
	if err != nil || n != len(part1) {
		t.Fatalf("Write part1: n=%d err=%v", n, err)
	}
	if diff := cmp.Diff(part1, ring.Tail()); diff != "" {
		t.Fatalf("Tail mismatch (-want +got):\n%s", diff)
	}
	if ring.Len() != len(part1) {
		t.Fatalf("ring.Len = %d, want %d", ring.Len(), len(part1))
	}

	// 2. Flood with 256 KiB of numbered 1 KiB chunks
	floodRing := NewRingBuffer(DefaultStderrRingCapacity)
	var fullStream bytes.Buffer

	for i := range 256 {
		chunk := []byte(fmt.Sprintf("[%04d:%s]\n", i, bytes.Repeat([]byte("X"), 1016)))
		fullStream.Write(chunk)
		if _, wErr := floodRing.Write(chunk); wErr != nil {
			t.Fatalf("Write flood chunk %d: %v", i, wErr)
		}
	}

	if floodRing.Len() != DefaultStderrRingCapacity {
		t.Fatalf("floodRing.Len = %d, want %d", floodRing.Len(), DefaultStderrRingCapacity)
	}

	tail := floodRing.Tail()
	if len(tail) != DefaultStderrRingCapacity {
		t.Fatalf("len(tail) = %d, want %d", len(tail), DefaultStderrRingCapacity)
	}

	expectedTail := fullStream.Bytes()[fullStream.Len()-DefaultStderrRingCapacity:]
	if !bytes.Equal(tail, expectedTail) {
		t.Fatal("Tail does not match the exact last 64 KiB of flooded stream")
	}
}
