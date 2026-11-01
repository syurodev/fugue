// lint:expect depguard math/rand

package depguard

import "math/rand"

func UseRand() int { return rand.Int() }
