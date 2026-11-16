package wall

import (
	"testing"
	"time"

	"fugue/internal/platform/clock"
)

func TestMeasures(t *testing.T) {
	start := time.Now()   // want `time.Now reads or measures the wall clock`
	_ = time.Since(start) // want `time.Since reads or measures the wall clock`
	_ = time.Until(start) // want `time.Until reads or measures the wall clock`
	var c clock.Real      // want `clock.Real reads or measures the wall clock`
	_ = c
	testing.Benchmark(func(b *testing.B) { _ = b.Elapsed() }) // want `testing.Benchmark reads or measures` `testing.Elapsed reads or measures`
}
