package workspace

import (
	"context"
	"errors"
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

func (s *Service) removeWorkspaceFiles(id, acceptedPath string) (result error) {
	if filepath.Clean(acceptedPath) != filepath.Clean(s.paths.AcceptedConfigPath(id)) {
		return NewError(ErrPathDenied, "accepted configuration path is outside workspace data")
	}
	acceptedRoot, err := s.openAcceptedCopyRemovalRoot(id)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := acceptedRoot.Close(); closeErr != nil {
			result = WrapError(ErrDeleteFailed, "accepted configuration directory could not be closed", errors.Join(result, closeErr))
		}
	}()
	workspaceRoot, err := s.openWorkspaceRemovalRoot()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := workspaceRoot.Close(); closeErr != nil {
			result = WrapError(ErrDeleteFailed, "workspace directory root could not be closed", errors.Join(result, closeErr))
		}
	}()
	workspaceInfo, err := workspaceRoot.Lstat(id)
	if err != nil || workspaceInfo.Mode()&os.ModeSymlink != 0 || !workspaceInfo.IsDir() {
		return WrapError(ErrPathDenied, "workspace directory is not a real directory", err)
	}
	if err := acceptedRoot.Remove("accepted.toml"); err != nil {
		return WrapError(ErrDeleteFailed, "accepted configuration copy could not be removed", err)
	}
	if err := workspaceRoot.RemoveAll(id); err != nil {
		return WrapError(ErrDeleteFailed, "workspace directory could not be removed", err)
	}
	return nil
}

func (s *Service) openWorkspaceRemovalRoot() (*os.Root, error) {
	expected, err := s.workspaceParentPath()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.paths.WorkspacesDir)
	if err != nil {
		return nil, WrapError(ErrPathDenied, "workspace parent could not be opened safely", err)
	}
	openedInfo, err := root.Stat(".")
	if err != nil {
		return rejectUnsafeRoot(root, "workspace parent could not be verified", err)
	}
	currentInfo, err := os.Lstat(s.paths.WorkspacesDir)
	if err != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() || !os.SameFile(openedInfo, currentInfo) {
		return rejectUnsafeRoot(root, "workspace parent changed while it was opened", err)
	}
	resolved, err := filepath.EvalSymlinks(s.paths.WorkspacesDir)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(expected) {
		return rejectUnsafeRoot(root, "workspace parent changed while it was opened", err)
	}
	return root, nil
}

func (s *Service) openAcceptedCopyRemovalRoot(id string) (*os.Root, error) {
	dataRoot, err := s.openDataRemovalRoot()
	if err != nil {
		return nil, err
	}
	workspacesInfo, workspaceInfo, err := acceptedCopyDirectoryInfo(dataRoot, id)
	if err != nil {
		return nil, err
	}
	root, err := dataRoot.OpenRoot(filepath.Join("workspaces", id))
	if err != nil {
		return rejectUnsafeRoot(dataRoot, "accepted configuration directory could not be opened safely", err)
	}
	if err := verifyAcceptedCopyRemovalRoot(root, dataRoot, id, workspacesInfo, workspaceInfo); err != nil {
		return nil, err
	}
	if err := dataRoot.Close(); err != nil {
		return rejectUnsafeRoot(root, "workspace data directory could not be closed", err)
	}
	return root, nil
}

func (s *Service) openDataRemovalRoot() (*os.Root, error) {
	dataInfo, err := os.Lstat(s.paths.DataDir)
	if err != nil || dataInfo.Mode()&os.ModeSymlink != 0 || !dataInfo.IsDir() {
		return nil, WrapError(ErrPathDenied, "workspace data directory is not a real directory", err)
	}
	dataRoot, err := os.OpenRoot(s.paths.DataDir)
	if err != nil {
		return nil, WrapError(ErrPathDenied, "workspace data directory could not be opened safely", err)
	}
	openedDataInfo, err := dataRoot.Stat(".")
	if err != nil {
		return rejectUnsafeRoot(dataRoot, "workspace data directory could not be verified", err)
	}
	currentDataInfo, err := os.Lstat(s.paths.DataDir)
	if err != nil || currentDataInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedDataInfo, currentDataInfo) {
		return rejectUnsafeRoot(dataRoot, "workspace data directory changed while it was opened", err)
	}
	return dataRoot, nil
}

func acceptedCopyDirectoryInfo(dataRoot *os.Root, id string) (os.FileInfo, os.FileInfo, error) {
	workspacesInfo, err := dataRoot.Lstat("workspaces")
	if err != nil || workspacesInfo.Mode()&os.ModeSymlink != 0 || !workspacesInfo.IsDir() {
		return nil, nil, rejectErrorRoot(dataRoot, "accepted configuration parent is not a real directory", err)
	}
	workspaceInfo, err := dataRoot.Lstat(filepath.Join("workspaces", id))
	if err != nil || workspaceInfo.Mode()&os.ModeSymlink != 0 || !workspaceInfo.IsDir() {
		return nil, nil, rejectErrorRoot(dataRoot, "accepted configuration directory is not a real directory", err)
	}
	return workspacesInfo, workspaceInfo, nil
}

func verifyAcceptedCopyRemovalRoot(root, dataRoot *os.Root, id string, workspacesInfo, workspaceInfo os.FileInfo) error {
	openedInfo, err := root.Stat(".")
	if err != nil {
		return rejectTwoUnsafeRoots(root, dataRoot, "accepted configuration directory could not be verified", err)
	}
	currentParent, parentErr := dataRoot.Lstat("workspaces")
	currentWorkspace, workspaceErr := dataRoot.Lstat(filepath.Join("workspaces", id))
	if parentErr != nil || currentParent.Mode()&os.ModeSymlink != 0 || !os.SameFile(workspacesInfo, currentParent) ||
		workspaceErr != nil || currentWorkspace.Mode()&os.ModeSymlink != 0 || !os.SameFile(workspaceInfo, currentWorkspace) || !os.SameFile(openedInfo, currentWorkspace) {
		return rejectTwoUnsafeRoots(root, dataRoot, "accepted configuration directory changed while it was opened", errors.Join(parentErr, workspaceErr))
	}
	return nil
}

func rejectErrorRoot(root *os.Root, message string, cause error) error {
	_, err := rejectUnsafeRoot(root, message, cause)
	return err
}

func rejectUnsafeRoot(root *os.Root, message string, cause error) (*os.Root, error) {
	if closeErr := root.Close(); closeErr != nil {
		cause = errors.Join(cause, closeErr)
	}
	return nil, WrapError(ErrPathDenied, message, cause)
}

func rejectTwoUnsafeRoots(root, parent *os.Root, message string, cause error) error {
	if closeErr := root.Close(); closeErr != nil {
		cause = errors.Join(cause, closeErr)
	}
	if closeErr := parent.Close(); closeErr != nil {
		cause = errors.Join(cause, closeErr)
	}
	return WrapError(ErrPathDenied, message, cause)
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
