//go:build darwin

package osappearance

/*
#include <stdint.h>
*/
import "C"

import (
	"context"
	"runtime/cgo"
	"sync/atomic"
)

type darwinSource struct {
	current atomic.Int32
}

type darwinWatcher struct {
	src    *darwinSource
	latest atomic.Int32
	notify chan struct{}
}

// New returns a Source backed by AppKit NSApp.effectiveAppearance on macOS.
func New() Source {
	return &darwinSource{}
}

func (s *darwinSource) Current() Appearance {
	return appearanceFromCode(s.current.Load())
}

func (s *darwinSource) Watch(ctx context.Context, onChange func(Appearance)) {
	w := &darwinWatcher{
		src:    s,
		notify: make(chan struct{}, 1),
	}
	handle := cgo.NewHandle(w)
	watchPtr := cgoStartAppearanceWatch(uintptr(handle))
	if watchPtr == 0 {
		handle.Delete()
		<-ctx.Done()
		return
	}
	defer cgoStopAppearanceWatch(watchPtr)

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
			next := appearanceFromCode(w.latest.Load())
			prev := appearanceFromCode(s.current.Swap(appearanceToCode(next)))
			if prev != next && onChange != nil {
				onChange(next)
			}
		}
	}
}

//export fugueOnAppearanceChange
func fugueOnAppearanceChange(handle C.uintptr_t, code C.int) {
	w, ok := cgo.Handle(handle).Value().(*darwinWatcher)
	if !ok || w == nil {
		return
	}
	w.latest.Store(int32(code))
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

//export fugueReleaseAppearanceHandle
func fugueReleaseAppearanceHandle(handle C.uintptr_t) {
	cgo.Handle(handle).Delete()
}
