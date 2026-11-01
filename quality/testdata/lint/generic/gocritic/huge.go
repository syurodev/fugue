// lint:expect gocritic hugeParam

package gocritic

type big struct{ data [64]int }

func Huge(b big) int { return b.data[0] }
