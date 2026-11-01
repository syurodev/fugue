// lint:ok exhaustruct_v5

package kernel

type StopCommand struct {
	Session string
}

func Complete() StopCommand { return StopCommand{Session: "s"} }

type plain struct{ A, B int }

func Plain() plain { return plain{A: 1} }
