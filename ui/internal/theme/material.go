package theme

import (
	"gioui.org/widget/material"
)

// newMaterialTheme builds the mode's single material theme from its tokens —
// Fg, Bg, ContrastBg (the action fill), ContrastFg and the shared shaper —
// so stock Gio widgets render with the app's colors. It runs once per mode,
// not per frame: material.NewTheme() parses its iconvg icons and would burn
// that work on every call. ContrastBg carries the action fill because that
// is the role material widgets use it for.
func (t *Theme) newMaterialTheme() *material.Theme {
	m := material.NewTheme()
	m.Shaper = t.shaper
	m.Fg = t.chrome.Fg
	m.Bg = t.chrome.AppBg
	m.ContrastBg = t.chrome.Action
	m.ContrastFg = t.chrome.ActionFg
	return m
}

// Material returns a shallow copy of the mode's material theme: callers may
// restyle their copy without touching the tokens or the cached icons, which
// stay shared so widget.Icon keeps its render cache across copies.
func (t *Theme) Material() *material.Theme {
	cp := *t.mat
	return &cp
}
