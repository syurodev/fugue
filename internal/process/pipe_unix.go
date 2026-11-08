//go:build unix

package process

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// outputPipe is the parent's read end of the child's stdout. It records
// when the stream ended (EOF or a read error), so Stop knows whether stdout
// has settled without reading on the consumer's behalf.
type outputPipe struct {
	file      *os.File
	ended     chan struct{}
	endOnce   sync.Once
	closeOnce sync.Once
	closeErr  error
}

func newOutputPipe(f *os.File) *outputPipe {
	return &outputPipe{file: f, ended: make(chan struct{})}
}

func (o *outputPipe) Read(b []byte) (int, error) {
	n, err := o.file.Read(b)
	if err != nil {
		o.endOnce.Do(func() { close(o.ended) })
		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}
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

// settled reports whether the stdout stream has ended (EOF or a read error).
func (o *outputPipe) settled() bool {
	select {
	case <-o.ended:
		return true
	default:
		return false
	}
}
