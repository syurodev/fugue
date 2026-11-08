package process

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

const testBootID = "boot-1"

func TestParseProcStatHandlesParensAndSpacesInComm(t *testing.T) {
	t.Parallel()

	line := "4242 (my (weird) )proc\nname) S 100 200 200 0 -1 4194304 10 0 0 0 5 2 0 0 20 0 1 0 987654321 1000 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0"
	got, err := parseProcStat(line, "boot-abc")
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	want := procEntry{
		pid:        4242,
		ppid:       100,
		pgid:       200,
		startToken: "boot-abc:987654321",
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateComparable(procEntry{})); diff != "" {
		t.Fatalf("parseProcStat mismatch (-want +got):\n%s", diff)
	}
}

func TestParseProcStatRejectsZombiesAndMalformedInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		line   string
		bootID string
	}{
		{
			name:   "zombie process",
			line:   "42 (defunct) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0",
			bootID: testBootID,
		},
		{
			name:   "missing close paren",
			line:   "42 (unclosed S 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345",
			bootID: testBootID,
		},
		{
			name:   "too few fields after comm",
			line:   "42 (cmd) S 1 42 42",
			bootID: testBootID,
		},
		{
			name:   "empty boot_id",
			line:   "42 (cmd) S 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0",
			bootID: "   ",
		},
		{
			name:   "non-positive pid",
			line:   "0 (cmd) S 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0",
			bootID: testBootID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseProcStat(tc.line, tc.bootID); !errors.Is(err, ErrIdentityMismatch) {
				t.Fatalf("expected ErrIdentityMismatch, got %v", err)
			}
		})
	}
}

func FuzzParseProcStat(f *testing.F) {
	f.Add("1234 (claude) R 1000 1234 1234 0 -1 4194304 10 0 0 0 1 1 0 0 20 0 1 0 55555 100 10", testBootID)
	f.Add("99 (a (b) c) S 1 99 99 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 1 0 0", "boot-2")
	f.Add("invalid stat line", "boot-3")
	f.Add("42 () Z 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20", "")

	f.Fuzz(func(t *testing.T, line, bootID string) {
		entry, err := parseProcStat(line, bootID)
		if err != nil {
			return
		}
		if entry.pid <= 0 || entry.ppid < 0 || entry.pgid < 0 || entry.startToken == "" {
			t.Fatalf("invalid procEntry returned without error: %+v", entry)
		}
	})
}
