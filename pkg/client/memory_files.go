package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const memoryExportFileMode = 0o600

// WriteMemoryExportFile writes exported Markdown bytes to path with mode 0600
// via a temp file in the same directory and an atomic rename.
func WriteMemoryExportFile(path string, content []byte) (retErr error) {
	clean := filepath.Clean(path)
	dir := filepath.Dir(clean)
	tmp, err := os.CreateTemp(dir, ".fugue-memory-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create export temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			if rmErr := os.Remove(tmpName); rmErr != nil && retErr == nil {
				retErr = fmt.Errorf("remove export temp file: %w", rmErr)
			}
		}
	}()
	if err := tmp.Chmod(memoryExportFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod export temp file: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write export temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync export temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close export temp file: %w", err)
	}
	if err := os.Rename(tmpName, clean); err != nil {
		return fmt.Errorf("rename export file: %w", err)
	}
	cleanup = false
	return nil
}

// ReadMemoryImportFile reads a picked Markdown import file, refusing any file
// larger than maxBytes (default MemoryImportMaxBytes when maxBytes <= 0).
func ReadMemoryImportFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = MemoryImportMaxBytes
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open memory import file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat memory import file: %w", err)
	}
	if info.Size() > maxBytes {
		return nil, ErrMemoryImportFileTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read memory import file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrMemoryImportFileTooLarge
	}
	return data, nil
}
