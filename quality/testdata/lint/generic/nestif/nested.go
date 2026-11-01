// lint:expect nestif

package nestif

func Nest(a, b, c, d, e bool) int {
	n := 0
	if a {
		if b {
			if c {
				if d {
					if e {
						n++
					}
				}
			}
		}
	}
	return n
}
