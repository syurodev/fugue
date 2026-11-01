package ids_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func epoch() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }

func TestUUIDv7HasVersionVariantAndTimestamp(t *testing.T) {
	t.Parallel()
	g := ids.NewUUIDv7(clock.NewFake(epoch()), ids.NewDeterministic("seed"))
	id, err := g.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !ids.IsUUID(id) {
		t.Fatalf("%q is not a canonical UUID", id)
	}
	if id[14] != '7' {
		t.Errorf("%q: version nibble = %c, want 7", id, id[14])
	}
	if !strings.ContainsRune("89ab", rune(id[19])) {
		t.Errorf("%q: variant nibble = %c, want one of 89ab", id, id[19])
	}
	// 2026-11-01T12:00:00Z is 1793534400000 ms, which is 0x01a196fb1600 in 48 bits.
	const wantPrefix = "01a196fb-1600"
	if got := id[:13]; got != wantPrefix {
		t.Errorf("%q: timestamp prefix = %s, want %s", id, got, wantPrefix)
	}
}

func TestUUIDv7SortsByCreationTime(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	g := ids.NewUUIDv7(f, ids.NewDeterministic("seed"))
	made := make([]string, 0, 5)
	for range 5 {
		id, err := g.NewID()
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, id)
		f.Advance(time.Millisecond)
	}
	sorted := append([]string(nil), made...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if diff := cmp.Diff(made, sorted); diff != "" {
		t.Fatalf("identifiers do not sort in creation order (-made +sorted):\n%s", diff)
	}
}

func TestDeterministicRepeatsForTheSameSeed(t *testing.T) {
	t.Parallel()
	read := func(seed string) []byte {
		b := make([]byte, 70)
		if err := ids.NewDeterministic(seed).Read(b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if diff := cmp.Diff(read("a"), read("a")); diff != "" {
		t.Fatalf("same seed gave different bytes:\n%s", diff)
	}
	if cmp.Equal(read("a"), read("b")) {
		t.Fatal("different seeds gave the same bytes")
	}
}

type failingRandom struct{}

var errNoEntropy = errors.New("no entropy")

func (failingRandom) Read([]byte) error { return errNoEntropy }

func TestNewIDPropagatesARandomnessFailure(t *testing.T) {
	t.Parallel()
	g := ids.NewUUIDv7(clock.NewFake(epoch()), failingRandom{})
	if _, err := g.NewID(); !errors.Is(err, errNoEntropy) {
		t.Fatalf("NewID = %v, want the randomness error", err)
	}
}

func TestCryptoRandomFillsTheBuffer(t *testing.T) {
	t.Parallel()
	b := make([]byte, 32)
	if err := (ids.CryptoRandom{}).Read(b); err != nil {
		t.Fatal(err)
	}
	if cmp.Equal(b, make([]byte, 32)) {
		t.Fatal("32 bytes of crypto randomness were all zero")
	}
}

func TestSequence(t *testing.T) {
	t.Parallel()
	s := ids.NewSequence("run")
	got := make([]string, 0, 3)
	for range 3 {
		id, err := s.NewID()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if diff := cmp.Diff([]string{"run-1", "run-2", "run-3"}, got); diff != "" {
		t.Fatalf("sequence mismatch (-want +got):\n%s", diff)
	}
}

func TestIsUUID(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"019a3f4a-8d00-7abc-8def-0123456789ab": true,
		"019A3F4A-8D00-7ABC-8DEF-0123456789AB": false,
		"019a3f4a8d007abc8def0123456789ab":     false,
		"019a3f4a-8d00-7abc-8def-0123456789a":  false,
		"019a3f4a-8d00-7abc-8def-0123456789ag": false,
		"019a3f4a_8d00-7abc-8def-0123456789ab": false,
		"":                                     false,
	}
	for in, want := range cases {
		if got := ids.IsUUID(in); got != want {
			t.Errorf("IsUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

// FuzzIsUUID checks that a string IsUUID accepts really has the canonical shape.
func FuzzIsUUID(f *testing.F) {
	f.Add("019a3f4a-8d00-7abc-8def-0123456789ab")
	f.Add("")
	f.Add("not-a-uuid")
	f.Fuzz(func(t *testing.T, s string) {
		if !ids.IsUUID(s) {
			return
		}
		if len(s) != 36 || strings.ToLower(s) != s || strings.Count(s, "-") != 4 {
			t.Fatalf("IsUUID accepted %q", s)
		}
	})
}
