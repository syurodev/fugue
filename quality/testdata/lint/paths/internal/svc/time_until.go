// lint:expect forbidigo time.Until

package svc

import "time"

func TimeUntil(t time.Time) time.Duration { return time.Until(t) }
