// lint:expect exhaustruct_v5 StartedEvent

package kernel

type StartedEvent struct {
	Run string
	At  int
}

func IncompleteEvent() StartedEvent { return StartedEvent{Run: "r"} }
