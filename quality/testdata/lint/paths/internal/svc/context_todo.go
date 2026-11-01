// lint:expect forbidigo context.TODO

package svc

import "context"

func CtxTodo() context.Context { return context.TODO() }
