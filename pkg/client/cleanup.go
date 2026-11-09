package client

import "io"

// closeBestEffort closes read-only or failed resources whose close cannot repair the operation.
func closeBestEffort(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}
