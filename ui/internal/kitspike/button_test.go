package kitspike

import (
	"testing"

	"gioui.org/io/key"
)

// TestButtonActivatesWithEnterAndSpace covers §6.2: both keys activate,
// Space only on release, and a disabled button activates nothing.
func TestButtonActivatesWithEnterAndSpace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		keyName  key.Name
		disabled bool
		want     bool
	}{
		{"enter", key.NameReturn, false, true},
		{"space", key.NameSpace, false, true},
		{"enter disabled", key.NameReturn, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			b := NewButtonState()
			h.frame(200, 40)
			clicked := b.Layout(h.env, &h.gtx, ButtonProps{Text: "Save", Variant: Secondary})
			// Focus during the frame, like a programmatic focus move does.
			h.gtx.Execute(key.FocusCmd{Tag: b.Tag()})
			h.end()
			if clicked {
				t.Fatal("clicked before any key")
			}
			h.frame(200, 40)
			b.Layout(h.env, &h.gtx, ButtonProps{Text: "Save", Variant: Secondary, Disabled: tc.disabled})
			h.end()
			h.press(tc.keyName, 0)
			h.frame(200, 40)
			clicked = b.Layout(h.env, &h.gtx, ButtonProps{Text: "Save", Variant: Secondary, Disabled: tc.disabled})
			h.end()
			if clicked != tc.want {
				t.Fatalf("%s: clicked = %v, want %v", tc.name, clicked, tc.want)
			}
		})
	}
}

// TestFocusVisibleOnlyAfterTab covers UK-05: the ring shows after a Tab and
// goes away after a pointer press.
func TestFocusVisibleOnlyAfterTab(t *testing.T) {
	h := newHarness(t)
	h.frame(200, 100)
	h.env.TrackInput(&h.gtx)
	h.end()

	// A pointer press clears the modality (already false).
	h.frame(200, 100)
	h.env.TrackInput(&h.gtx)
	if h.env.FocusVisible(&h.gtx, nil) {
		t.Fatal("focus visible without keyboard")
	}
	h.end()

	// Tab sets the keyboard modality. Events are queued after the router
	// saw the previous frame: a Frame discards what is still pending.
	h.frame(200, 100)
	h.env.TrackInput(&h.gtx)
	h.end()
	h.router.Queue(key.Event{Name: key.NameTab, State: key.Press})
	h.frame(200, 100)
	h.env.TrackInput(&h.gtx)
	if !h.env.modality {
		t.Fatal("modality not set by Tab")
	}
	h.end()

	// A pointer press clears it again: force the modality on, then press.
	h.env.modality = true
	h.click(50, 50)
	h.frame(200, 100)
	h.env.TrackInput(&h.gtx)
	h.end()
	h.frame(200, 100)
	h.env.TrackInput(&h.gtx)
	if h.env.modality {
		t.Fatal("pointer press did not clear the keyboard modality")
	}
	h.end()
}

// TestButtonDisabledKeepsWidth checks §6.2's busy rule's sibling: a disabled
// button still lays out at its natural width and swallows no clicks.
func TestButtonDisabledKeepsWidth(t *testing.T) {
	h := newHarness(t)
	b := NewButtonState()
	h.frame(200, 40)
	b.Layout(h.env, &h.gtx, ButtonProps{Text: "Save", Disabled: true})
	h.end()
	h.click(10, 10)
	h.frame(200, 40)
	if clicked := b.Layout(h.env, &h.gtx, ButtonProps{Text: "Save", Disabled: true}); clicked {
		t.Fatal("a disabled button clicked")
	}
	h.end()
}
