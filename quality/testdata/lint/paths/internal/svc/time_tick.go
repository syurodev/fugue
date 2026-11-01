// lint:expect forbidigo time.Tick

package svc

import "time"

func TimeTick() <-chan time.Time { return time.Tick(time.Second) }
