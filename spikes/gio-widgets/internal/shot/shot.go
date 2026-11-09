// Package shot renders a widget to a PNG without a window (headless GPU), so
// layout can be checked on a machine without a display.
package shot

import (
	"image"
	"image/png"
	"os"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// Render lays out fn twice (the first frame warms the caches) and writes the
// second as a PNG.
func Render(path string, size image.Point, fn func(gtx layout.Context)) error {
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return err
	}
	defer w.Release()
	var ops op.Ops
	for range 2 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Constraints: layout.Exact(size), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
		fn(gtx)
		if err := w.Frame(&ops); err != nil {
			return err
		}
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := w.Screenshot(img); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
