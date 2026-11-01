// lint:expect depguard github.com/pkg/errors

package depguard

import "github.com/pkg/errors"

func UseErrors() { errors.Use() }
