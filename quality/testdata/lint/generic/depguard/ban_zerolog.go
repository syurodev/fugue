// lint:expect depguard github.com/rs/zerolog

package depguard

import "github.com/rs/zerolog"

func UseZerolog() { zerolog.Use() }
