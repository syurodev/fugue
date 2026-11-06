package hostapi

import (
	"io"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
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
