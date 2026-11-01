// lint:expect prealloc

package prealloc

func Collect(xs []int) []int {
	var out []int
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}
