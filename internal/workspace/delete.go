package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/ids"
)

// DeleteWorkspace removes the workspace record and accepted copy before its
// Fugue-owned directory. It never follows a workspace symlink or a root link.
func (s *Service) DeleteWorkspace(ctx context.Context, id, expectedHash, confirmName string) (*Workspace, error) {
	if !isWorkspaceID(id) {
		return nil, NewError(ErrIDInvalid, "workspace id is invalid")
	}
	if expectedHash == "" || confirmName == "" {
		return nil, NewError(ErrConfirmationRequired, "expected_hash and confirm_name are required")
	}
	if err := s.validateWorkspaceDirectory(id); err != nil {
		return nil, err
	}
	ws, err := s.deletionCandidate(ctx, id, expectedHash, confirmName)
	if err != nil {
		return nil, err
	}
	acceptedPath, err := s.acceptedCopyForDeletion(id)
	if err != nil {
		return nil, err
	}
	deleteErr := s.store.DeleteExpected(ctx, id, expectedHash)
	if deleteErr != nil {
		if CodeOf(deleteErr) == ErrConfigChanged {
			return nil, deleteErr
		}
		return nil, WrapError(ErrDeleteFailed, "workspace record could not be removed", deleteErr)
	}
	if err := s.removeWorkspaceFiles(id, acceptedPath); err != nil {
		return ws, err
	}
	return ws, nil
}

func (s *Service) deletionCandidate(ctx context.Context, id, expectedHash, confirmName string) (*Workspace, error) {
	rec, _, raw, _, _, err := s.loadWorkspaceInputs(ctx, id)
	if err != nil {
		return nil, err
	}
	if hashErr := checkExpectedHash(raw, rec.ConfigHash, expectedHash); hashErr != nil {
		return nil, hashErr
	}
	ws, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}
	if ws.Name != confirmName {
		return nil, NewError(ErrConfirmationRequired, "confirm_name does not match workspace name")
	}
	return ws, nil
}

func (s *Service) acceptedCopyForDeletion(id string) (string, error) {
	acceptedPath := s.paths.AcceptedConfigPath(id)
	info, err := os.Lstat(acceptedPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", WrapError(ErrDeleteFailed, "accepted configuration copy is unavailable", err)
	}
	return acceptedPath, nil
}

func (s *Service) removeWorkspaceFiles(id, acceptedPath string) error {
	if err := os.Remove(acceptedPath); err != nil {
		return WrapError(ErrDeleteFailed, "accepted configuration copy could not be removed", err)
	}
	if err := os.RemoveAll(s.paths.WorkspaceDir(id)); err != nil {
		return WrapError(ErrDeleteFailed, "workspace directory could not be removed", err)
	}
	return nil
}

func (s *Service) validateWorkspaceDirectory(id string) error {
	workspaceDir := s.paths.WorkspaceDir(id)
	info, err := os.Lstat(workspaceDir)
	if os.IsNotExist(err) {
		return NewError(ErrNotFound, "workspace was not found")
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return WrapError(ErrPathDenied, "workspace directory is not a real directory", err)
	}
	resolved, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		return WrapError(ErrPathDenied, "workspace directory could not be resolved", err)
	}
	parent, err := s.workspaceParentPath()
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Join(filepath.Clean(parent), id) || filepath.Clean(workspaceDir) != filepath.Join(filepath.Clean(s.paths.WorkspacesDir), id) {
		return NewError(ErrPathDenied, "workspace directory is outside its configured parent")
	}
	return nil
}

func (s *Service) workspaceParentPath() (string, error) {
	parentInfo, err := os.Lstat(s.paths.WorkspacesDir)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return "", WrapError(ErrPathDenied, "workspace parent is not a real directory", err)
	}
	parent, err := filepath.EvalSymlinks(s.paths.WorkspacesDir)
	if err != nil {
		return "", WrapError(ErrPathDenied, "workspace parent directory could not be resolved", err)
	}
	home, err := filepath.EvalSymlinks(s.paths.FugueHome)
	if err != nil {
		return "", WrapError(ErrPathDenied, "Fugue home directory could not be resolved", err)
	}
	if filepath.Clean(parent) != filepath.Join(filepath.Clean(home), "workspaces") {
		return "", NewError(ErrPathDenied, "workspace directory is outside Fugue home")
	}
	return parent, nil
}

func isWorkspaceID(id string) bool {
	return ids.IsUUID(id) && len(id) == 36 && id[14] == '7' && strings.ContainsRune("89ab", rune(id[19]))
}
