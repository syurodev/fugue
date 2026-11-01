// lint:expect forbidigo rand.Read

package svc

import "crypto/rand"

func RandRead(b []byte) (int, error) { return rand.Read(b) }
