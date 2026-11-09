package hostconn

import "io"

func closeHandle(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}
