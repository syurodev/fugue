// lint:expect err113

package err113

import "errors"

func Fail() error { return errors.New("dynamic") }
