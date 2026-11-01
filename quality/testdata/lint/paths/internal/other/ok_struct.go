// lint:ok exhaustruct_v5

package other

type LooseCommand struct {
	A string
	B string
}

func Loose() LooseCommand { return LooseCommand{A: "a"} }
