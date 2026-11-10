package theme

import (
	"gioui.org/widget/material"
)

// Material returns a material theme seeded from this mode's tokens — Fg, Bg,
// ContrastBg, ContrastFg and the shared shaper — so stock Gio widgets render
// with the app's colors. ContrastBg carries the action fill because that is
// the role material widgets use it for.
func (t *Theme) Material() *material.Theme {
	m := material.NewTheme()
	m.Shaper = t.shaper
	m.Fg = t.chrome.Fg
	m.Bg = t.chrome.AppBg
	m.ContrastBg = t.chrome.Action
	m.ContrastFg = t.chrome.ActionFg
	return m
}
