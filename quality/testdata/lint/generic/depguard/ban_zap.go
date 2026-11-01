// lint:expect depguard go.uber.org/zap

package depguard

import "go.uber.org/zap"

func UseZap() { zap.Use() }
