// lint:expect errcheck

package errcheck

import "os"

func Blank() { _ = os.Remove("x") }
