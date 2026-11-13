package main

import (
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// play lays the autoplaying gallery out at 60 Hz without a GPU until the
// script reaches until, calling probe after every frame.
func play(t *testing.T, until time.Duration, probe func(elapsed time.Duration, g *gallery)) *gallery {
	t.Helper()
	th, err := newTheme(false)
	if err != nil {
		t.Fatal(err)
	}
	g := newGallery(&th, true)
	var ops op.Ops
	start := time.Unix(1_700_000_000, 0)
	for now := start; now.Sub(start) <= until; now = now.Add(frameStep) {
		ops.Reset()
		gtx := frameContext(&ops, layout.Exact(image.Pt(windowW, windowH)), 1)
		gtx.Now = now
		g.frame(&gtx)
		if probe != nil {
			probe(now.Sub(start), g)
		}
	}
	return g
}

func TestInterruptedMenuTurnsRoundWithoutJumping(t *testing.T) {
	t.Parallel()
	var last float32
	peak := float32(0)
	play(t, ms(6500), func(elapsed time.Duration, g *gallery) {
		if elapsed < ms(6000) {
			return
		}
		p := g.menu.open.Progress()
		if elapsed > ms(6000) && (p-last > 0.3 || last-p > 0.3) {
			t.Fatalf("menu progress jumped from %v to %v at %v", last, p, elapsed)
		}
		peak = max(peak, p)
		last = p
	})
	if peak <= 0.1 || peak >= 1 {
		t.Fatalf("interrupted menu peaked at %v, want it to turn round part way open", peak)
	}
	if last > 0.01 {
		t.Fatalf("menu still open at %v after the interrupt", last)
	}
}

func TestSettledGalleryOnlyWakesForTheNextCue(t *testing.T) {
	t.Parallel()
	g := play(t, autoplayLoop-ms(300), nil)
	at, ok := g.driver.Next()
	if !ok || at.IsZero() {
		t.Fatalf("settled gallery Next = %v %v, want only the loop restart", at, ok)
	}
}

func TestStreamingMessageKeepsFramesComing(t *testing.T) {
	t.Parallel()
	g := play(t, ms(3400), nil)
	if at, ok := g.driver.Next(); !ok || !at.IsZero() {
		t.Fatalf("gallery with a streaming message Next = %v %v, want an immediate frame", at, ok)
	}
}
