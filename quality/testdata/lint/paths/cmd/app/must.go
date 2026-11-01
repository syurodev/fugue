// lint:ok forbidigo

package main

func mustPositive(n int) int {
	if n < 0 {
		panic("negative")
	}
	return n
}
