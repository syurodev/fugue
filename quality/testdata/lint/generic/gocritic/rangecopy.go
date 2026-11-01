// lint:expect gocritic rangeValCopy

package gocritic

type item struct{ data [32]int }

func RangeCopy(items []item) int {
	n := 0
	for _, it := range items {
		n += it.data[0]
	}
	return n
}
