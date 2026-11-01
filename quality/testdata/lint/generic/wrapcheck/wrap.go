// lint:expect wrapcheck

package wrapcheck

import "os"

func Remove() error { return os.Remove("x") }
