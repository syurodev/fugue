package appwin

import (
	"image"
	"sync"

	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// macDoubleClickAction names the macOS user preference for double-clicking a
// window's title strip.
type macDoubleClickAction uint8

const (
	macDoubleClickNone macDoubleClickAction = iota
	macDoubleClickZoom
	macDoubleClickMinimize
)

// macWindowMetrics holds the native macOS title-strip geometry in dp, measured
// from NSWindow.frame, NSWindow.contentLayoutRect, and the zoom button frame.
type macWindowMetrics struct {
	BarHeightDp     float32
	ZoomButtonFrame image.Rectangle
}

// macTitleBarPlatform is the platform boundary for applying native macOS window
// chrome, measuring the native title-strip geometry, reading the double-click
// preference, and syncing the active drag rectangle.
type macTitleBarPlatform interface {
	applyTitleBar(viewPtr uintptr, fullscreen bool, onMetrics func(macWindowMetrics))
	doubleClickAction() macDoubleClickAction
	setDragRect(viewPtr uintptr, rect image.Rectangle)
}

type macMetricsState struct {
	mu       sync.Mutex
	metrics  macWindowMetrics
	measured bool
}

func (s *macMetricsState) set(m macWindowMetrics) {
	s.mu.Lock()
	s.metrics = m
	s.measured = true
	s.mu.Unlock()
}

func (s *macMetricsState) get() (macWindowMetrics, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metrics, s.measured
}

func (b *titleBar) setMacMetrics(m macWindowMetrics) {
	if b == nil {
		return
	}
	b.mac.set(m)
	if b.invalidate != nil {
		b.invalidate()
	}
}

func (b *titleBar) macBarHeight(gtx *layout.Context) int {
	if b.goos == osDarwin && !b.fullscreen {
		if m, ok := b.mac.get(); ok && m.BarHeightDp > 0 {
			return max(gtx.Dp(unit.Dp(m.BarHeightDp)), 1)
		}
	}
	return gtx.Dp(theme.TopBarHeight)
}

func (b *titleBar) macLeftInset(gtx *layout.Context) int {
	if m, ok := b.mac.get(); ok && m.ZoomButtonFrame.Max.X > 0 {
		return gtx.Dp(unit.Dp(float32(m.ZoomButtonFrame.Max.X))) + gtx.Dp(theme.SpaceMD)
	}
	return gtx.Dp(theme.MacTrafficLightInset)
}

func (b *titleBar) darwinDoubleClickAction() system.Action {
	if b.platform == nil {
		return b.toggleMaximize()
	}
	switch b.platform.doubleClickAction() {
	case macDoubleClickZoom:
		return b.toggleMaximize()
	case macDoubleClickMinimize:
		return system.ActionMinimize
	case macDoubleClickNone:
		return 0
	default:
		return b.toggleMaximize()
	}
}

func (b *titleBar) syncPlatformDragRect(gtx *layout.Context, area image.Rectangle) {
	if b.platform == nil || b.nativeView == 0 {
		return
	}
	var dpRect image.Rectangle
	if !area.Empty() && gtx.Metric.PxPerDp > 0 {
		inv := 1 / gtx.Metric.PxPerDp
		dpRect = image.Rect(
			int(float32(area.Min.X)*inv+0.5),
			int(float32(area.Min.Y)*inv+0.5),
			int(float32(area.Max.X)*inv+0.5),
			int(float32(area.Max.Y)*inv+0.5),
		)
	}
	if dpRect == b.lastDragRectDp {
		return
	}
	b.lastDragRectDp = dpRect
	b.platform.setDragRect(b.nativeView, dpRect)
}
