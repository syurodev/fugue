package kit

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
)

func TestFilterBarDebounceAndClear(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bar := NewFilterBarState()
	props := FilterBarProps{
		SearchPlaceholder: "Search entries…",
		Debounce:          200 * time.Millisecond,
		Filters: []FilterSelect{
			{
				ID:          "kind",
				Placeholder: "Kind",
				Options: []SelectOption{
					{ID: "", Label: "All kinds"},
					{ID: "fact", Label: "Fact"},
				},
			},
		},
	}

	// Initial frame emits nothing.
	h.frameSize(t, 700, 120)
	_, res, err := bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("FilterBar initial layout: %v", err)
	}
	if res.Changed || res.State.Query != "" {
		t.Fatalf("unexpected initial change: %+v", res)
	}
	h.router.Frame(&h.ops)

	// Simulate user typing into search field via widget text mutation + focus/edit.
	bar.search.SetText("auth")
	h.frameSize(t, 700, 120)
	_, res, err = bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("FilterBar typing layout: %v", err)
	}
	if res.Changed {
		t.Fatalf("expected debounced query not to emit immediately, got %+v", res)
	}
	h.router.Frame(&h.ops)

	// Before debounce elapses (100ms), still not emitted.
	h.now = h.now.Add(100 * time.Millisecond)
	h.frameSize(t, 700, 120)
	_, res, err = bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("FilterBar mid-debounce: %v", err)
	}
	if res.Changed {
		t.Fatalf("expected query to wait for 200ms debounce, got %+v", res)
	}
	h.router.Frame(&h.ops)

	// After 200ms total, debounced query emits.
	h.now = h.now.Add(120 * time.Millisecond)
	h.frameSize(t, 700, 120)
	_, res, err = bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("FilterBar post-debounce: %v", err)
	}
	if !res.Changed || res.State.Query != "auth" {
		t.Fatalf("expected debounced Changed=true Query=auth, got %+v", res)
	}
	h.router.Frame(&h.ops)

	// Clicking Clear filters emits cleared state immediately.
	props.Filters[0].Selected = "fact"
	h.frameSize(t, 700, 120)
	_, _, _ = bar.Layout(h.env, &h.gtx, props)
	h.gtx.Execute(key.FocusCmd{Tag: bar.ClearTag()})
	h.router.Frame(&h.ops)

	h.router.Queue(
		key.Event{Name: key.NameReturn, State: key.Press},
		key.Event{Name: key.NameReturn, State: key.Release},
	)
	h.frameSize(t, 700, 120)
	_, res, err = bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("FilterBar clear: %v", err)
	}
	if !res.Changed || res.State.Query != "" || res.State.Selected["kind"] != "" {
		t.Fatalf("expected cleared state, got %+v", res)
	}
}

func TestFilterBarSelectEmitsImmediately(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bar := NewFilterBarState()
	props := FilterBarProps{
		Filters: []FilterSelect{
			{
				ID:          "author",
				Placeholder: "Author",
				Options: []SelectOption{
					{ID: "user", Label: "User"},
					{ID: "agent", Label: "Agent"},
				},
			},
		},
	}

	h.frameSize(t, 600, 120)
	_, _, err := bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatal(err)
	}
	h.gtx.Execute(key.FocusCmd{Tag: bar.SelectTag(0)})
	h.router.Frame(&h.ops)

	// Open Select via keyboard Down arrow and verify dropdown opens.
	h.router.Queue(
		key.Event{Name: key.NameDownArrow, State: key.Press},
	)
	h.frameSize(t, 600, 300)
	_, _, err = bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatal(err)
	}
	h.router.Frame(&h.ops)

	if !bar.selects[0].IsOpen() {
		t.Fatal("expected Select dropdown to open on Down arrow")
	}
	// Press Down + Return to choose the second option (agent) or first option (user).
	h.router.Queue(
		key.Event{Name: key.NameReturn, State: key.Press},
		key.Event{Name: key.NameReturn, State: key.Release},
	)
	h.frameSize(t, 600, 300)
	_, res, err := bar.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.State.Selected["author"] != "user" {
		t.Fatalf("expected author=user selection, got %+v", res)
	}
	_ = image.Point{}
	_ = pointer.Press
}
