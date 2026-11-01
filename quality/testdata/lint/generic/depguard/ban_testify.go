// lint:expect depguard github.com/stretchr/testify/assert

package depguard

import "github.com/stretchr/testify/assert"

func UseTestify() { assert.Equal() }
