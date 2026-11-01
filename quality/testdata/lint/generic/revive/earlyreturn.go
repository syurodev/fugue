// lint:expect revive indent-error-flow

package revive

func Early(a int) int {
	if a > 0 {
		return 1
	} else {
		return 2
	}
}
