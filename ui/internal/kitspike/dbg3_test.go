package kitspike

import (
	"testing"

	"gioui.org/io/key"
)

func TestDebugEditorEvents(t *testing.T) {
	h := newHarness(t)
	f := NewTextFieldState()
	h.frame(300, 100)
	f.Layout(h.env, &h.gtx, FieldProps{})
	h.end()
	h.gtx.Execute(key.FocusCmd{Tag: f.Tag()})
	t.Logf("focused right after Execute: %v", h.gtx.Focused(f.Tag()))
	h.end()
	h.frame(300, 100)
	f.Layout(h.env, &h.gtx, FieldProps{})
	t.Logf("focused after next layout: %v", h.gtx.Focused(f.Tag()))
	h.end()
	t.Logf("focused after end: %v", h.gtx.Focused(f.Tag()))
	h.router.Queue(key.EditEvent{Range: key.Range{Start: 0, End: 0}, Text: "abc"})
	h.frame(300, 100)
	f.Layout(h.env, &h.gtx, FieldProps{})
	h.end()
	t.Logf("text after edit: %q, focused: %v", f.Text(), h.gtx.Focused(f.Tag()))
}
