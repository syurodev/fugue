package clean

import (
	"runtime"
	"testing"
	"time"
)

// Bounded work with no way out may yield between rounds.
func TestPacingLoops(t *testing.T) {
	for i := range 1000 {
		if i%7 == 0 {
			runtime.Gosched()
		}
	}
	for i := 0; i < 10; i++ {
		runtime.Gosched()
	}
	for _, v := range []int{1, 2} {
		_ = v
		time.Sleep(0)
	}
}

// A break that only leaves an inner statement does not let the loop end early.
func TestInnerBreakIsNotAnExit(t *testing.T) {
	for i := range 5 {
		switch i {
		case 1:
			break
		}
		runtime.Gosched()
	}
}

// A single yield or sleep is bounded; so is a wait in a function a helper
// owns, because the helper is checked where its loop is.
func TestSingleYield(t *testing.T) {
	runtime.Gosched()
	time.Sleep(time.Millisecond)
}

func TestClosureInPacingLoop(t *testing.T) {
	for range 3 {
		func() { runtime.Gosched() }()
	}
}
