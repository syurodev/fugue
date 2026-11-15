// Package kit is the shared component kit of the app: the only place that
// draws controls (ui-kit UK-01). It carries the window frame environment —
// the theme, the shaper, the F10.6 motion driver, the UI scale and the
// focus-visible modality — and the primitives: text from the theme's type
// tokens, Lucide icons with strokes cached per dp size in a never-reset
// ops buffer, an indeterminate spinner that asks for frames only while
// drawn, the elevation.float shadow, and the pressable controls (Button
// and IconButton, F10.9). Fields, menus and the rest of kit v1 land in
// F10.10–F10.12; every component's doc comment points at its ui-kit
// § section.
//
// The kit knows nothing about the Host API, vendors or modules (UK-10): it
// takes colors and sizes from ui/internal/theme and reports what happened
// during the frame; it never stores a callback (UK-06).
//
// Owner: F10.8. Allowed callers: ui internal packages and the app commands;
// the core module never imports this module.
package kit
