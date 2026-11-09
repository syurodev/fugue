package view

import (
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
)

// Streamer appends deltas to the last entry 30 times a second, and a new
// entry every NewEvery (0 = never), like the host does for a running turn.
type Streamer struct {
	V        *View
	NewEvery time.Duration
	On       bool
	last     time.Time
	lastNew  time.Time
}

// Tick adds a delta when 33 ms have passed since the previous one.
func (s *Streamer) Tick() {
	now := time.Now()
	v := s.V
	if s.NewEvery > 0 && now.Sub(s.lastNew) > s.NewEvery {
		s.lastNew = now
		v.Add(gen.Entry{Kind: "assistant", Head: "**Fugue** streaming", Body: []flow.Block{{}}})
	}
	if now.Sub(s.last) < 33*time.Millisecond {
		return
	}
	s.last = now
	e := v.Entries[len(v.Entries)-1]
	e.Kind = "assistant"
	b := e.Body
	if len(b) == 0 {
		b = []flow.Block{{}}
	}
	b[0].Runs = append(b[0].Runs, gen.Inline("delta **ghép** `chữ` văn bản ")...)
	e.SetBody(b)
}

// Interactive handles Ctrl+S (toggle streaming) for the by-hand runs.
func (s *Streamer) Interactive(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(key.Filter{Required: key.ModShortcut, Name: "S"})
		if !ok {
			break
		}
		if k, isKey := ev.(key.Event); isKey && k.State == key.Press {
			s.On = !s.On
		}
	}
	if s.On {
		s.Tick()
		gtx.Execute(op.InvalidateCmd{})
	}
}
