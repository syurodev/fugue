// lint:ok forbidigo

package ids

import "crypto/rand"

func Fill(b []byte) (int, error) { return rand.Read(b) }
