// lint:expect depguard math/rand/v2

package depguard

import "math/rand/v2"

func UseRandV2() int { return rand.Int() }
