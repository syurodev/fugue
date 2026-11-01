// lint:ok forbidigo

package clock

import "time"

func Now() time.Time { return time.Now() }

func Sleep() { time.Sleep(time.Second) }
