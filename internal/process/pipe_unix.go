//go:build unix

package process

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

// outputPipe is the parent's read end of the child's stdout. It records
// whether a consumer is blocked in Read and when the stream ended, so Stop can
// tell a consumer waiting on a pipe held open by an escaped process from one
// that simply has not read yet, without reading on the consumer's behalf.
type outputPipe struct {
	file      *os.File
	inRead    atomic.Int32
	ended     chan struct{}
	endOnce   sync.Once
	closeOnce sync.Once
	closeErr  error
}

func newOutputPipe(f *os.File) *outputPipe {
	return &outputPipe{file: f, ended: make(chan struct{})}
}

func (o *outputPipe) Read(b []byte) (int, error) {
	o.inRead.Add(1)
	n, err := o.file.Read(b)
	o.inRead.Add(-1)
	if err != nil {
		o.endOnce.Do(func() { close(o.ended) })
		return n, fmt.Errorf("read stdout pipe: %w", err)
	}
	return n, nil
}

// Close closes the read end once; Stop and the LineReader at EOF may both
// call it.
func (o *outputPipe) Close() error {
	o.closeOnce.Do(func() {
		if err := o.file.Close(); err != nil {
			o.closeErr = fmt.Errorf("close stdout pipe: %w", err)
		}
	})
	return o.closeErr
}

// settled reports that no consumer is blocked on the pipe: either the stream
// ended or nobody is reading right now.
func (o *outputPipe) settled() bool {
	select {
	case <-o.ended:
		return true
	default:
		return o.inRead.Load() == 0
	}
}
