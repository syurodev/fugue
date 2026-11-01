// lint:expect ineffassign

package ineffassign

func Dead() int {
	x := 1
	x = 2
	_ = x
	return 0
}
