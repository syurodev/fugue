// lint:expect govet

package govet

import "fmt"

func Wrong() string { return fmt.Sprintf("%d", "text") }
