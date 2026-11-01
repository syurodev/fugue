// lint:expect forbidigo time.Since

package svc

import "time"

func TimeSince(t time.Time) time.Duration { return time.Since(t) }
