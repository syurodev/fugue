package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// CheckAndEnforcePermissions verifies that rootPath and all its contents belong
// to the current process UID (failing closed with workspace.ownership_invalid otherwise),
// and tightens any permissive group or world permissions to 0700 (directories) or
// 0600 (files), logging an audit message for every adjusted entry.
func CheckAndEnforcePermissions(
	ctx context.Context,
	rootPath string,
	statUID func(os.FileInfo) (int, error),
	logger *slog.Logger,
) error {
	if statUID == nil {
		statUID = fileSysUID
	}
	if logger == nil {
		logger = slog.Default()
	}

	walkErr := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return WrapError(ErrOwnershipInvalid, "access "+path, err)
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return WrapError(ErrOwnershipInvalid, "stat "+path, infoErr)
		}
		return checkEntry(ctx, path, info, statUID, logger)
	})
	if walkErr != nil {
		return WrapError(ErrOwnershipInvalid, "walk directory "+rootPath, walkErr)
	}
	return nil
}

func checkEntry(
	ctx context.Context,
	path string,
	info os.FileInfo,
	statUID func(os.FileInfo) (int, error),
	logger *slog.Logger,
) error {
	uid, err := statUID(info)
	if err != nil {
		return WrapError(ErrOwnershipInvalid, "stat UID for "+path, err)
	}
	if uid != os.Getuid() {
		return NewError(ErrOwnershipInvalid, fmt.Sprintf("entry %s belongs to foreign UID %d (expected UID %d)", path, uid, os.Getuid()))
	}
	return enforcePerm(ctx, path, info.IsDir(), info.Mode().Perm(), logger)
}

func enforcePerm(ctx context.Context, path string, isDir bool, mode os.FileMode, logger *slog.Logger) error {
	target, kind := filePerm, "file"
	if isDir {
		target, kind = dirPerm, "directory"
	}
	if mode&0o077 == 0 && mode == target {
		return nil
	}
	if err := os.Chmod(path, target); err != nil {
		return WrapError(ErrOwnershipInvalid, "chmod "+path, err)
	}
	logger.WarnContext(ctx, "tightened workspace "+kind+" permissions",
		"path", path,
		"old_mode", fmt.Sprintf("%04o", mode),
		"new_mode", fmt.Sprintf("%04o", target),
	)
	return nil
}
