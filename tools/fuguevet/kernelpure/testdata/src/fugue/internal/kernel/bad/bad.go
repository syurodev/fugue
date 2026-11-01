package bad

import (
	"errors"
	"fmt"
	"os"   // want `kernel import "os" is not allowlisted`
	"sync" // want `kernel import "sync" is not allowlisted`
	"time"

	"example.com/dep" // want `kernel import "example.com/dep" is not allowlisted`
)

var counter = 1 // want `package variable counter in the kernel`

var errNotSentinel = fmt.Errorf("wrapped: %w", errors.ErrUnsupported) // want `package variable errNotSentinel in the kernel`

func Now() time.Time { return time.Now() } // want `time\.Now in the kernel`

func Wait() { time.Sleep(time.Second) } // want `time\.Sleep in the kernel`

func Print() { fmt.Println("x") } // want `fmt\.Println in the kernel`

func Spawn() { go func() {}() } // want `go statement in the kernel`

func Impure() (int, int) {
	var mu sync.Mutex
	_ = mu
	return len(os.Args), dep.X
}
