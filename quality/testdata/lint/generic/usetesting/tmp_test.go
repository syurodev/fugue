// lint:expect usetesting

package usetesting

import (
	"context"
	"os"
	"testing"
)

func TestTemp(t *testing.T) {
	t.Parallel()
	_, _ = os.MkdirTemp("", "x")
	_ = context.Background()
}
