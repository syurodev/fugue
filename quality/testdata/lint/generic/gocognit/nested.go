// lint:expect gocognit

package gocognit

func Nested(a, b, c, d, e, f bool) int {
	n := 0
	if a {
		if b {
			if c {
				if d {
					if e {
						if f {
							n++
						}
					}
				}
			}
		}
	}
	return n
}
