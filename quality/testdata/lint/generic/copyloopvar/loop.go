// lint:expect copyloopvar

package copyloopvar

func Copy(xs []int) int {
	sum := 0
	for _, v := range xs {
		v := v
		sum += v
	}
	return sum
}
