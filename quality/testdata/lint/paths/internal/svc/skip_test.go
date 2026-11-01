// lint:expect forbidigo Skip

package svc

import "testing"

func TestSkips(t *testing.T) {
	t.Parallel()
	t.Skip("no")
}
