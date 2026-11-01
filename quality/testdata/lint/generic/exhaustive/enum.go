// lint:expect exhaustive

package exhaustive

type colour int

const (
	red colour = iota
	green
)

func Name(c colour) string {
	switch c {
	case red:
		return "red"
	}
	return ""
}
