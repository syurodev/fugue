// lint:expect exhaustruct_v5 StartCommand

package kernel

type StartCommand struct {
	Session string
	Run     string
}

func Incomplete() StartCommand { return StartCommand{Session: "s"} }
