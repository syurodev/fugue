// lint:expect reassign

package reassign

import "io"

func Break() { io.EOF = nil }
