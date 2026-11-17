//go:build perf

package perf

import (
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/secrets"
)

const knownAPIToken = "sk-live-abcdef1234567890"

func mixedText(size int) string {
	line := "ordinary mixed text 1234\nAPI_TOKEN=abcd1234\n"
	return strings.Repeat(line, size/len(line)+1)[:size]
}

// secretText builds size bytes of filler interleaved with a token pattern and
// the known credential.
func secretText(size int) string {
	githubToken := "ghp_" + strings.Repeat("a1B2c3D4e5", 3) + "F6g7H8"
	var b strings.Builder
	for b.Len() < size {
		b.WriteString("word " + githubToken + " tail " + knownAPIToken + " end\n")
	}
	return b.String()
}

// TestFindOneMiBLinearBound holds the scan to a linear cost: 4x the input may
// cost at most 6x the time. A quadratic scan costs about 16x.
func TestFindOneMiBLinearBound(t *testing.T) {
	d := secrets.New([]secrets.KnownValue{{Value: redact.NewSecret("abcd1234"), Kind: secrets.KindVault}}, secrets.Options{})
	short, long := mixedText(256<<10), mixedText(1<<20)
	got := medianRatio(t, "find 256 KiB to 1 MiB", func() { d.Find(short) }, func() { d.Find(long) })
	if got > 6 {
		t.Fatalf("median 1 MiB scan cost %.2fx the 256 KiB scan, want at most 6x", got)
	}
}

// TestRedactionCostIsLinear holds masking to a linear cost: 16x the input at a
// linear cost is about 16x the time; a quadratic detector costs at least 256x.
func TestRedactionCostIsLinear(t *testing.T) {
	d := secrets.New([]secrets.KnownValue{{Value: redact.NewSecret(knownAPIToken), Kind: secrets.KindCredential}}, secrets.Options{})
	redactAll := func(text string) func() {
		return func() { secrets.Mask(text, d.Find(text)) }
	}
	got := medianRatio(t, "redact 64 KiB to 1 MiB", redactAll(secretText(64<<10)), redactAll(secretText(1<<20)))
	if got > 64 {
		t.Fatalf("median 1 MiB redaction cost %.2fx the 64 KiB cost, want at most 64x", got)
	}
}

// TestFindOneMiBAllocationBound holds a 1 MiB scan to 256 MiB allocated per
// call, the median over the measurements.
func TestFindOneMiBAllocationBound(t *testing.T) {
	d := secrets.New([]secrets.KnownValue{{Value: redact.NewSecret("abcd1234"), Kind: secrets.KindVault}}, secrets.Options{})
	text := mixedText(1 << 20)
	allocs := make([]int64, 0, measurements)
	for range measurements {
		res := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				d.Find(text)
			}
		})
		t.Logf("1 MiB scan allocates %d bytes per call", res.AllocedBytesPerOp())
		allocs = append(allocs, res.AllocedBytesPerOp())
	}
	if got := median(allocs); got > 256<<20 {
		t.Fatalf("median 1 MiB scan allocation %d bytes, want at most %d", got, 256<<20)
	}
}
