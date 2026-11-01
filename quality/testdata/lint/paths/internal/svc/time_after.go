// lint:expect forbidigo time.After

package svc

import "time"

func TimeAfter() <-chan time.Time { return time.After(time.Second) }
