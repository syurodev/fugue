// lint:expect gosec G306

package gosec

import "os"

func Perm() error { return os.WriteFile("x", nil, 0o777) }
