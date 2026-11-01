// lint:expect forbidigo testing.Short

package svc

import "testing"

func TestShort(t *testing.T) {
	t.Parallel()
	_ = testing.Short()
}
