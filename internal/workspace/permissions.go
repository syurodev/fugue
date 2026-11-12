package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	dirPerm, filePerm os.FileMode = 0o700, 0o600
)

type pathEntry struct {
	path     string
	optional bool
}

// CheckAndEnforcePermissions verifies ownership and permissions for the Fugue home,
// workspaces directory, workspace directory, scratch directory, fugue.toml, WORKSPACE.md,
// and AGENTS.md (if present). It does not walk into scratch contents, rejects any symlink
// with workspace.ownership_invalid, and tightens permissive modes to 0700/0600.
func CheckAndEnforcePermissions(
	ctx context.Context,
	paths Paths,
	workspaceID string,
	statUID func(os.FileInfo) (int, error),
	logger *slog.Logger,
) error {
	if statUID == nil {
		statUID = fileSysUID
	}
	if logger == nil {
		logger = slog.Default()
	}

	wsDir := paths.WorkspaceDir(workspaceID)
	entries := []pathEntry{
		{path: paths.FugueHome},
		{path: paths.WorkspacesDir},
		{path: wsDir},
		{path: paths.ScratchDir(workspaceID)},
		{path: paths.ConfigPath(workspaceID)},
		{path: paths.WorkspaceMDPath(workspaceID)},
		{path: filepath.Join(wsDir, "AGENTS.md"), optional: true},
	}

	for _, entry := range entries {
		if err := checkEntry(ctx, entry, statUID, logger); err != nil {
			return err
		}
	}
	return nil
}

func checkEntry(
	ctx context.Context,
	entry pathEntry,
	statUID func(os.FileInfo) (int, error),
	logger *slog.Logger,
) error {
	info, err := os.Lstat(entry.path)
	if err != nil {
		if entry.optional && os.IsNotExist(err) {
			return nil
		}
		return WrapError(ErrOwnershipInvalid, "lstat "+entry.path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return NewError(ErrOwnershipInvalid, "symlink not allowed in workspace: "+entry.path)
	}
	uid, err := statUID(info)
	if err != nil {
		return WrapError(ErrOwnershipInvalid, "stat UID for "+entry.path, err)
	}
	if uid != os.Getuid() {
		return NewError(ErrOwnershipInvalid, fmt.Sprintf("entry %s belongs to foreign UID %d (expected UID %d)", entry.path, uid, os.Getuid()))
	}
	return enforcePerm(ctx, entry.path, info.IsDir(), info.Mode().Perm(), logger)
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
	logger.WarnContext(ctx, "tightened workspace "+kind+" permissions", "path", path, "old_mode", fmt.Sprintf("%04o", mode), "new_mode", fmt.Sprintf("%04o", target))
	return nil
}
