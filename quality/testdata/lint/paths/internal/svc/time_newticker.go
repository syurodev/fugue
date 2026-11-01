// lint:expect forbidigo time.NewTicker

package svc

import "time"

func TimeNewTicker() *time.Ticker { return time.NewTicker(time.Second) }
