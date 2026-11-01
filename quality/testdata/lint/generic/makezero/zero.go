// lint:expect makezero

package makezero

func Grow() []int {
	s := make([]int, 3)
	return append(s, 1)
}
