// lint:expect errorlint

package errorlint

import "io"

func IsEOF(err error) bool { return err == io.EOF }
