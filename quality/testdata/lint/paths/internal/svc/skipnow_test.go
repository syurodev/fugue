// lint:expect forbidigo SkipNow

package svc

import "testing"

func TestSkipNow(t *testing.T) {
	t.Parallel()
	t.SkipNow()
}
