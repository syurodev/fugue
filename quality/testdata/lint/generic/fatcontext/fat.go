// lint:expect fatcontext

package fatcontext

import "context"

type key struct{}

func Fat(ctx context.Context) context.Context {
	for range 3 {
		ctx = context.WithValue(ctx, key{}, "v")
	}
	return ctx
}
