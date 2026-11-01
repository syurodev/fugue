// lint:ok gochecknoglobals

package gochecknoglobals

import "errors"

var ErrSentinel = errors.New("sentinel")
