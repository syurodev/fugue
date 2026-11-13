package hostapi

import (
	"io"
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// The vendor tests run this test binary as the Claude CLI.
	if playFakeVendor(os.Args) {
		os.Exit(0)
	}
	goleak.VerifyTestMain(m)
}

// closeQuietly closes c during test teardown, where nothing can act on the
// error.
func closeQuietly(c io.Closer) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		return
	}
}
