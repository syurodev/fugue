// lint:expect unparam

package unparam

import "fmt"

func Caller() { fmt.Println(always(true)) }

func always(f bool) bool {
	if f {
		return true
	}
	return true
}
