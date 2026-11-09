package client

import (
	"errors"
	"fmt"
	"io"
)

// WriteBoundedLog drains a host's output after writing at most limit bytes.
func WriteBoundedLog(dst io.Writer, src io.Reader, limit int64) error {
	_, writeErr := io.Copy(dst, io.LimitReader(src, limit))
	_, drainErr := io.Copy(io.Discard, src)
	if err := errors.Join(writeErr, drainErr); err != nil {
		return fmt.Errorf("write or drain host log: %w", err)
	}
	return nil
}
