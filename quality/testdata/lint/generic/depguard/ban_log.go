// lint:expect depguard 'log'

package depguard

import "log"

func UseLog() { _ = log.Default }
