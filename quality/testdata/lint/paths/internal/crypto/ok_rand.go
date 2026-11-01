// lint:ok forbidigo

package crypto

import "crypto/rand"

func Fill(b []byte) (int, error) { return rand.Read(b) }
