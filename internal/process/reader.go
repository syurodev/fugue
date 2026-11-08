package process

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxFrameSize is the hard 8 MiB limit on a single line frame per vendor-runtime §8.
	MaxFrameSize = 8 * 1024 * 1024

	defaultReadChunkSize = 64 * 1024
)

// LineReader reads continuous line-based frames from an io.Reader.
// Frames exceeding maxFrameSize cause ReadLine to return ErrFrameTooLarge
// without allocating unbounded memory.
type LineReader struct {
	r            io.Reader
	maxFrameSize int
	buf          []byte
	bufRead      int
	bufEnd       int
	line         bytes.Buffer
	eof          bool
}

// NewLineReader creates a LineReader with a maximum frame size cap.
func NewLineReader(r io.Reader, maxFrameSize int) *LineReader {
	return &LineReader{
		r:            r,
		maxFrameSize: maxFrameSize,
		buf:          make([]byte, defaultReadChunkSize),
	}
}

// ReadLine returns the next line frame without trailing \r or \n.
// It returns io.EOF only when no more bytes remain and the stream has ended.
// If a line exceeds maxFrameSize before a newline, it returns ErrFrameTooLarge.
func (lr *LineReader) ReadLine() ([]byte, error) {
	lr.line.Reset()

	for {
		if frame, found, err := lr.consumeBuffered(); err != nil {
			return nil, err
		} else if found {
			return frame, nil
		}

		if lr.eof {
			return lr.drainFinalFrame()
		}

		if err := lr.readMore(); err != nil {
			return nil, err
		}
	}
}

func (lr *LineReader) consumeBuffered() ([]byte, bool, error) {
	if lr.bufRead >= lr.bufEnd {
		return nil, false, nil
	}

	idx := bytes.IndexByte(lr.buf[lr.bufRead:lr.bufEnd], '\n')
	if idx >= 0 {
		chunk := lr.buf[lr.bufRead : lr.bufRead+idx]
		if lr.line.Len()+len(chunk) > lr.maxFrameSize {
			return nil, false, ErrFrameTooLarge
		}
		lr.line.Write(chunk)
		lr.bufRead += idx + 1 // skip newline
		return finalizeFrame(lr.line.Bytes()), true, nil
	}

	chunk := lr.buf[lr.bufRead:lr.bufEnd]
	if lr.line.Len()+len(chunk) > lr.maxFrameSize {
		return nil, false, ErrFrameTooLarge
	}
	lr.line.Write(chunk)
	lr.bufRead = lr.bufEnd
	return nil, false, nil
}

func (lr *LineReader) drainFinalFrame() ([]byte, error) {
	if lr.line.Len() > 0 {
		frame := finalizeFrame(lr.line.Bytes())
		lr.line.Reset()
		return frame, nil
	}
	return nil, io.EOF
}

func (lr *LineReader) readMore() error {
	n, err := lr.r.Read(lr.buf)
	lr.bufRead = 0
	lr.bufEnd = n

	if err == nil {
		return nil
	}
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("read stream: %w", err)
	}
	lr.eof = true
	return lr.closeUnderlying()
}

func (lr *LineReader) closeUnderlying() error {
	if closer, ok := lr.r.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			return fmt.Errorf("close stream: %w", err)
		}
	}
	return nil
}

func finalizeFrame(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
