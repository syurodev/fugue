package main

import (
	"image"
	"math"
	"strconv"
	"time"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// Feed tuning.
const (
	maxMessages = 4
	streamRate  = 70 // characters per second
	streamTail  = 14 // characters over which new text fades in
)

func sampleReplies() []string {
	return []string{
		"Ran the race tests on ./internal/app: three packages pass, one test is flaky under load.",
		"The migration is reversible now; both indexes come back on rollback.",
		"Opened the pull request and linked the roadmap row.",
		"CI is green on the new head. Nothing else is pending.",
	}
}

// message is one entry of the feed.
type message struct {
	id    int
	text  string
	born  time.Time
	enter motion.Presence
}

// feed inserts messages at the top: neighbours slide down (Slots), the new
// one grows in and its text streams with a soft fading edge, the oldest
// leaves when the feed is full, and the token counter rolls its digits.
type feed struct {
	send    pressButton
	msgs    []*message
	slots   motion.Slots[int]
	height  motion.Follower
	tokens  int
	digits  []motion.Follower // per decimal place, following tokens / 10^place
	counter int
}

func newFeed() feed {
	return feed{send: newPressButton(), msgs: nil, slots: motion.NewSlots[int](motion.Smooth()), height: motion.NewFollower(motion.Smooth()), tokens: 12480, digits: nil, counter: 0}
}

// add inserts the next sample message at the top.
func (f *feed) add(now time.Time) {
	replies := sampleReplies()
	m := &message{id: f.counter, text: replies[f.counter%len(replies)], born: now, enter: motion.NewPresence(motion.Smooth(), motion.Exit())}
	m.enter.Show(true)
	f.counter++
	f.tokens += 900 + 137*f.counter
	f.msgs = append([]*message{m}, f.msgs...)
	live := 0
	for _, old := range f.msgs {
		if old.enter.Shown() {
			live++
			if live > maxMessages {
				old.enter.Show(false)
			}
		}
	}
}

func (f *feed) layout(gtx *layout.Context, g *gallery) layout.Dimensions {
	dims, clicked := f.send.layout(gtx, g, "Add message")
	if clicked {
		f.add(g.now)
	}
	at(gtx, image.Pt(dims.Size.X+gtx.Dp(16), 0), func(gtx *layout.Context) layout.Dimensions {
		return f.drawCounter(gtx, g, dims.Size.Y)
	})
	top := dims.Size.Y + gtx.Dp(14)
	list := f.layoutList(gtx, g, top)
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, top+list), Baseline: 0}
}

// layoutList draws the messages below top and returns the animated height.
func (f *feed) layoutList(gtx *layout.Context, g *gallery, top int) int {
	w := gtx.Constraints.Max.X
	f.slots.Begin()
	y, kept := 0, f.msgs[:0]
	m := op.Record(gtx.Ops)
	for _, msg := range f.msgs {
		g.keep(msg.enter.Advance(g.dt))
		if !msg.enter.Visible() {
			continue
		}
		kept = append(kept, msg)
		drawnY := f.slots.Place(msg.id, float32(y), g.dt)
		h := f.drawMessage(gtx, g, msg, image.Pt(0, int(drawnY)), w)
		y += h + gtx.Dp(8)
	}
	call := m.Stop()
	f.msgs = kept
	g.keep(f.slots.End())
	h := f.height.Follow(float32(y), g.dt)
	g.keep(f.height.Moving())
	defer clip.Rect(image.Rect(-gtx.Dp(8), top-gtx.Dp(10), w+gtx.Dp(8), top+int(h)+gtx.Dp(8))).Push(gtx.Ops).Pop()
	defer op.Offset(image.Pt(0, top)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return int(h)
}

// drawMessage draws one message at p and returns its full height.
func (f *feed) drawMessage(gtx *layout.Context, g *gallery, msg *message, p image.Point, w int) int {
	c := g.th.Chrome()
	pad := gtx.Dp(12)
	reveal := float32(g.now.Sub(msg.born).Seconds()) * streamRate
	total := float32(len([]rune(msg.text)))
	if reveal < total+streamTail {
		g.keep(true)
	}
	rec := op.Record(gtx.Ops)
	txt := at(gtx, image.Pt(pad, pad), func(gtx *layout.Context) layout.Dimensions {
		return streamText(gtx, g.sh, body(), w-2*pad, msg.text, reveal, streamTail, c.Fg)
	})
	call := rec.Stop()
	h := max(txt.Size.Y, gtx.Sp(13)) + 2*pad
	prog := msg.enter.Progress()
	defer op.Offset(p.Add(image.Pt(0, -int(float32(gtx.Dp(8))*(1-prog))))).Push(gtx.Ops).Pop()
	defer scaleAround(gtx.Ops, f32.Pt(float32(w)/2, float32(h)/2), 0.97+0.03*prog).Pop()
	defer paint.PushOpacity(gtx.Ops, min(max(prog, 0), 1)).Pop()
	r := image.Rect(0, 0, w, h)
	fillRRect(gtx.Ops, r, gtx.Dp(10), c.SurfaceRaised)
	strokeRRect(gtx.Ops, r, gtx.Dp(10), float32(gtx.Dp(1)), c.Border)
	call.Add(gtx.Ops)
	return h
}

// drawCounter draws "N tokens" with each digit rolling to its new value: a
// digit place follows tokens / 10^place, so a carry rolls forward through
// 9 → 0 the way a mechanical counter does.
func (f *feed) drawCounter(gtx *layout.Context, g *gallery, rowH int) layout.Dimensions {
	c := g.th.Chrome()
	digits := strconv.Itoa(f.tokens)
	for len(f.digits) < len(digits) {
		f.digits = append(f.digits, motion.NewFollower(motion.Smooth()))
	}
	colW := textWidth(gtx, g.sh, label(), "0") + gtx.Dp(1)
	lineH := gtx.Sp(13) * 13 / 10
	y := (rowH - lineH) / 2
	x := 0
	for i := range len(digits) {
		place := len(digits) - 1 - i
		target := float32(f.tokens / pow10(place))
		v := f.digits[place].Follow(target, g.dt)
		g.keep(f.digits[place].Moving())
		at(gtx, image.Pt(x, y), func(gtx *layout.Context) layout.Dimensions {
			return rollDigit(gtx, g, v, colW, lineH)
		})
		x += colW
		if place > 0 && place%3 == 0 {
			x += at(gtx, image.Pt(x, y), func(gtx *layout.Context) layout.Dimensions {
				return textAt(gtx, g.sh, label(), colW, ",", c.Fg)
			}).Size.X
		}
	}
	x += at(gtx, image.Pt(x+gtx.Dp(6), y), func(gtx *layout.Context) layout.Dimensions {
		return textAt(gtx, g.sh, body(), gtx.Constraints.Max.X, "tokens", c.FgMuted)
	}).Size.X + gtx.Dp(6)
	return layout.Dimensions{Size: image.Pt(x, rowH), Baseline: 0}
}

// rollDigit draws the digit column at value v: the current digit slides up
// and out while the next slides in from below.
func rollDigit(gtx *layout.Context, g *gallery, v float32, colW, lineH int) layout.Dimensions {
	c := g.th.Chrome()
	base := float32(math.Floor(float64(v)))
	frac := v - base
	d0 := int(base) % 10
	defer clip.Rect(image.Rect(0, 0, colW, lineH)).Push(gtx.Ops).Pop()
	for k, d := range []int{d0, (d0 + 1) % 10} {
		off := (float32(k) - frac) * float32(lineH)
		alpha := 1 - min(abs32(off)/float32(lineH), 1)
		if alpha <= 0 {
			continue
		}
		s := strconv.Itoa(d)
		x := (colW - textWidth(gtx, g.sh, label(), s)) / 2
		at(gtx, image.Pt(x, int(off)), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, label(), colW, s, fade(c.Fg, alpha))
		})
	}
	return layout.Dimensions{Size: image.Pt(colW, lineH), Baseline: 0}
}

func pow10(n int) int {
	p := 1
	for range n {
		p *= 10
	}
	return p
}
