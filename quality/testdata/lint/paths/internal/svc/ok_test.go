// lint:ok forbidigo

package svc

import (
	"context"
	"testing"
)

func TestBackground(t *testing.T) {
	t.Parallel()
	_ = context.Background()
	if false {
		panic("never")
	}
}
