// lint:expect forbidigo time.Sleep

package svc

import "time"

func TimeSleep() { time.Sleep(time.Second) }
