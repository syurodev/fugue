// lint:expect forbidigo context.Background

package svc

import "context"

func CtxBackground() context.Context { return context.Background() }
