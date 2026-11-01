// lint:expect forbidigo time.NewTimer

package svc

import "time"

func TimeNewTimer() *time.Timer { return time.NewTimer(time.Second) }
