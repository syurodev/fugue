// lint:expect intrange

package intrange

func Count() int {
	n := 0
	for i := 0; i < 10; i++ {
		n++
	}
	return n
}
