// lint:expect wastedassign

package wastedassign

func Waste() int {
	x := 1
	x = 2
	return x
}
