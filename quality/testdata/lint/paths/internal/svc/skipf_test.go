// lint:expect forbidigo Skipf

package svc

import "testing"

func TestSkipf(t *testing.T) {
	t.Parallel()
	t.Skipf("no")
}
