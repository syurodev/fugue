// lint:expect errcheck

package errcheck

import "os"

func Unchecked() { os.Remove("x") }
