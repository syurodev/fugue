// lint:expect gosec G401

package gosec

import "crypto/md5"

func Weak() []byte { return md5.New().Sum(nil) }
