package waits

import (
	"runtime"
	"testing"
	"time"
)

func TestBareLoop(t *testing.T) {
	for {
		if Ready() {
			return
		}
		runtime.Gosched() // want `runtime.Gosched in a loop that can only end when its condition holds`
	}
}

func TestConditionLoop(t *testing.T) {
	for !Ready() {
		time.Sleep(time.Millisecond) // want `time.Sleep in a loop that can only end`
	}
}

// A large count is not a deadline: the loop still returns when the state
// appears and otherwise spins as long as the scheduler lets it.
func TestCountedWait(t *testing.T) {
	for range 100000 {
		if Ready() {
			return
		}
		runtime.Gosched() // want `runtime.Gosched in a loop that can only end`
	}
	t.Fatal("never ready")
}

func TestCountedLoopWithBreak(t *testing.T) {
	for i := 0; i < 10; i++ {
		if Ready() {
			break
		}
		time.Sleep(time.Millisecond) // want `time.Sleep in a loop that can only end`
	}
}

func TestNestedLoopOfAWaitingLoop(t *testing.T) {
	for {
		for range 3 {
			runtime.Gosched() // want `runtime.Gosched in a loop that can only end`
		}
		if Ready() {
			return
		}
	}
}

func TestClosureLoop(t *testing.T) {
	wait := func() {
		for !Ready() {
			runtime.Gosched() // want `runtime.Gosched in a loop that can only end`
		}
	}
	wait()
}
