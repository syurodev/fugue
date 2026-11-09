// Package composer is spike 2: the multi-line prompt box on widget.Editor.
// Enter submits, Shift+Enter breaks the line, undo/redo and word movement are
// the Editor's own; the slash menu is the part built here.
package composer

import (
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Command is one slash entry; Group is the vendor or "fugue:".
type Command struct{ Name, Group, Help string }

// Commands is a stand-in for the vendor and fugue: groups.
var Commands = []Command{
	{"/clear", "vendor", "Start a fresh context"}, {"/compact", "vendor", "Summarise the context"},
	{"/model", "vendor", "Pick the model"}, {"/review", "vendor", "Review the diff"}, {"/init", "vendor", "Write the project notes"},
	{"/fugue:team", "fugue:", "Propose a team"}, {"/fugue:status", "fugue:", "Show session status"},
	{"/fugue:budget", "fugue:", "Show quota use"}, {"/fugue:integrate", "fugue:", "Integrate the team branch"},
}

// Composer owns the editor and the slash menu.
type Composer struct {
	Ed       widget.Editor
	Sent     []string
	Menu     []Command // current matches; empty = closed
	Sel      int
	rows     [16]widget.Clickable
	th       *material.Theme
	lastText string
}

// New returns a composer in submit mode.
func New(th *material.Theme) *Composer {
	c := &Composer{th: th}
	c.Ed.Submit = true // Enter submits; Shift+Enter inserts a line break
	return c
}

// matches lists the commands for the first word when the text starts with "/".
func matches(text string) []Command {
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, " \n") {
		return nil
	}
	var out []Command
	for _, c := range Commands {
		if strings.HasPrefix(c.Name, text) && c.Name != text {
			out = append(out, c)
		}
	}
	return out
}

func (c *Composer) accept(i int) {
	c.Ed.SetText(c.Menu[i].Name + " ")
	n := c.Ed.Len()
	c.Ed.SetCaret(n, n)
	c.Menu = nil
}

// Update handles the frame's input. The menu steals Up, Down, Tab, Enter and
// Escape only while it is open, so the Editor keeps them otherwise.
func (c *Composer) Update(gtx layout.Context) {
	if len(c.Menu) > 0 {
		for {
			ev, ok := gtx.Event(
				key.Filter{Focus: &c.Ed, Name: key.NameUpArrow}, key.Filter{Focus: &c.Ed, Name: key.NameDownArrow},
				key.Filter{Focus: &c.Ed, Name: key.NameTab}, key.Filter{Focus: &c.Ed, Name: key.NameReturn},
				key.Filter{Focus: &c.Ed, Name: key.NameEnter}, key.Filter{Focus: &c.Ed, Name: key.NameEscape})
			if !ok {
				break
			}
			k, isKey := ev.(key.Event)
			if !isKey || k.State != key.Press || len(c.Menu) == 0 {
				continue
			}
			switch k.Name {
			case key.NameUpArrow:
				c.Sel = (c.Sel + len(c.Menu) - 1) % len(c.Menu)
			case key.NameDownArrow:
				c.Sel = (c.Sel + 1) % len(c.Menu)
			case key.NameTab, key.NameReturn, key.NameEnter:
				c.accept(c.Sel)
			case key.NameEscape:
				c.Menu = nil
			}
		}
	}
	for {
		ev, ok := c.Ed.Update(gtx)
		if !ok {
			break
		}
		if s, isSubmit := ev.(widget.SubmitEvent); isSubmit && strings.TrimSpace(s.Text) != "" {
			c.Sent = append(c.Sent, s.Text)
			c.Ed.SetText("")
			c.Menu = nil
		}
	}
	if t := c.Ed.Text(); t != c.lastText { // only re-filter when the text changed
		c.lastText = t
		c.Menu, c.Sel = matches(t), 0
	}
}

// Layout draws the menu (above) and the editor, grown up to maxLines.
func (c *Composer) Layout(gtx layout.Context, maxLines int) layout.Dimensions {
	c.Update(gtx)
	for i := range c.Menu {
		if i < len(c.rows) && c.rows[i].Clicked(gtx) {
			c.accept(i)
			break
		}
	}
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Start}.Layout(gtx,
		layout.Rigid(c.menu),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lh := gtx.Sp(c.th.TextSize) * 3 / 2
			gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, lh*maxLines+gtx.Dp(16))
			return c.box(gtx)
		}))
}

func (c *Composer) box(gtx layout.Context) layout.Dimensions {
	in := layout.UniformInset(unit.Dp(8))
	m := op.Record(gtx.Ops)
	dims := in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		ed := material.Editor(c.th, &c.Ed, "Message, or / for commands")
		return ed.Layout(gtx)
	})
	call := m.Stop()
	rr := gtx.Dp(8)
	st := clip.UniformRRect(image.Rectangle{Max: dims.Size}, rr).Push(gtx.Ops)
	paint.ColorOp{Color: c.th.ContrastBg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	st.Pop()
	in2 := image.Rectangle{Min: image.Pt(1, 1), Max: dims.Size.Sub(image.Pt(1, 1))}
	st = clip.UniformRRect(in2, rr).Push(gtx.Ops)
	paint.ColorOp{Color: c.th.Bg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	st.Pop()
	call.Add(gtx.Ops)
	return dims
}

func (c *Composer) menu(gtx layout.Context) layout.Dimensions {
	if len(c.Menu) == 0 {
		return layout.Dimensions{}
	}
	var items []layout.FlexChild
	for i, m := range c.Menu[:min(len(c.Menu), len(c.rows))] {
		items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.rows[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if i == c.Sel {
					paint.FillShape(gtx.Ops, c.th.ContrastBg, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, gtx.Dp(26))}.Op())
				}
				return layout.Inset{Left: 10, Top: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := material.Body2(c.th, m.Name+"   "+m.Group+" · "+m.Help)
					l.Color = c.th.Fg
					return l.Layout(gtx)
				})
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
}
