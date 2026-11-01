// lint:expect thelper

package thelper

import "testing"

func helper(t *testing.T) { _ = t }

func TestUsesHelper(t *testing.T) {
	t.Parallel()
	helper(t)
}
