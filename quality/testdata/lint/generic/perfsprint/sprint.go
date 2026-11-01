// lint:expect perfsprint

package perfsprint

import "fmt"

func Itoa(i int) string { return fmt.Sprintf("%d", i) }
