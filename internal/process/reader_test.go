package process

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestLineReaderFailsOnFrameTooLarge(t *testing.T) {
	t.Parallel()

	t.Run("normal frames and distinct EOF", func(t *testing.T) {
		t.Parallel()

		input := []byte("{\"type\":\"init\"}\n{\"type\":\"delta\",\"content\":\"hi\"}\r\n{\"type\":\"done\"}\n")
		lr := NewLineReader(bytes.NewReader(input), MaxFrameSize)

		line1, err := lr.ReadLine()
		if err != nil || string(line1) != "{\"type\":\"init\"}" {
			t.Fatalf("line1: got %q, err %v", string(line1), err)
		}

		line2, err := lr.ReadLine()
		if err != nil || string(line2) != "{\"type\":\"delta\",\"content\":\"hi\"}" {
			t.Fatalf("line2: got %q, err %v", string(line2), err)
		}

		line3, err := lr.ReadLine()
		if err != nil || string(line3) != "{\"type\":\"done\"}" {
			t.Fatalf("line3: got %q, err %v", string(line3), err)
		}

		line4, err := lr.ReadLine()
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected io.EOF on completed stream, got line=%q, err=%v", string(line4), err)
		}
	})

	t.Run("frame exceeding 8 MiB limit fails typed", func(t *testing.T) {
		t.Parallel()

		// Stream starts with a line exceeding 8 MiB by 1024 bytes
		hugeLine := bytes.Repeat([]byte("B"), MaxFrameSize+1024)
		hugeLine = append(hugeLine, '\n')

		lr := NewLineReader(bytes.NewReader(hugeLine), MaxFrameSize)
		_, err := lr.ReadLine()
		if !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("expected ErrFrameTooLarge for line over 8 MiB, got %v", err)
		}
	})

	t.Run("bounded memory on massive line", func(t *testing.T) {
		t.Parallel()

		// An endless stream of bytes without newline
		infiniteBytes := bytes.NewReader(bytes.Repeat([]byte("Z"), 20*1024*1024))
		lr := NewLineReader(infiniteBytes, MaxFrameSize)

		_, err := lr.ReadLine()
		if !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("expected ErrFrameTooLarge, got %v", err)
		}
	})
}
