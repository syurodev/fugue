// lint:expect contextcheck

package contextcheck

import "context"

func Outer(ctx context.Context) { inner(ctx); other() }

func inner(ctx context.Context) { _ = ctx }

func other() { inner(context.TODO()) }

func Caller(ctx context.Context) { other(); _ = ctx }
