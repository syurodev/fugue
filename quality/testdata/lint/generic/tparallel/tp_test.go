// lint:expect tparallel

package tparallel

import "testing"

func TestSubtests(t *testing.T) {
	t.Parallel()
	t.Run("sub", func(t *testing.T) { _ = t })
}
