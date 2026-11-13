package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/ids"
)

const acceptedConfigName = "accepted.toml"

// DeleteWorkspace removes the workspace record, its accepted-copy directory and
// its Fugue-owned directory. It never follows a workspace symlink or a root
// link. Both directories are opened and verified before the record is removed,
// so a refused deletion keeps the record and can be retried.
func (s *Service) DeleteWorkspace(ctx context.Context, id, expectedHash, confirmName string) (ws *Workspace, result error) {
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
	roots, err := s.openRemovalRoots(id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := roots.close(); closeErr != nil {
			result = WrapError(ErrDeleteFailed, "workspace directories could not be closed", errors.Join(result, closeErr))
		}
	}()
	if deleteErr := s.store.DeleteExpected(ctx, id, expectedHash); deleteErr != nil {
		if CodeOf(deleteErr) == ErrConfigChanged {
			return nil, deleteErr
		}
		return nil, WrapError(ErrDeleteFailed, "workspace record could not be removed", deleteErr)
	}
	if err := roots.remove(id); err != nil {
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

// removalRoots holds the two parent directories a deletion removes one entry
// from: <DataDir>/workspaces (accepted copy) and the workspaces directory.
type removalRoots struct {
	accepted   *os.Root
	workspaces *os.Root
}

// openRemovalRoots opens both parents and checks that each workspace entry is
// a real directory and that the accepted copy is a regular file.
func (s *Service) openRemovalRoots(id string) (*removalRoots, error) {
	accepted, err := s.openAcceptedParentRoot()
	if err != nil {
		return nil, err
	}
	workspaces, err := s.openWorkspaceRemovalRoot()
	if err != nil {
		return nil, closeRootsDenied("workspace parent could not be opened safely", err, accepted)
	}
	roots := &removalRoots{accepted: accepted, workspaces: workspaces}
	if err := roots.verifyEntries(id); err != nil {
		return nil, closeRootsDenied("workspace directories are not safe to remove", err, accepted, workspaces)
	}
	return roots, nil
}

func (r *removalRoots) verifyEntries(id string) error {
	for _, entry := range []struct {
		root *os.Root
		what string
	}{{r.accepted, "accepted configuration directory"}, {r.workspaces, "workspace directory"}} {
		info, err := entry.root.Lstat(id)
		if err != nil || !isRealDir(info) {
			return WrapError(ErrPathDenied, entry.what+" is not a real directory", err)
		}
	}
	info, err := r.accepted.Lstat(filepath.Join(id, acceptedConfigName))
	if err != nil || !info.Mode().IsRegular() {
		return WrapError(ErrDeleteFailed, "accepted configuration copy is unavailable", err)
	}
	return nil
}

func (r *removalRoots) remove(id string) error {
	if err := r.accepted.RemoveAll(id); err != nil {
		return WrapError(ErrDeleteFailed, "accepted configuration directory could not be removed", err)
	}
	if err := r.workspaces.RemoveAll(id); err != nil {
		return WrapError(ErrDeleteFailed, "workspace directory could not be removed", err)
	}
	return nil
}

func (r *removalRoots) close() error {
	return errors.Join(r.accepted.Close(), r.workspaces.Close())
}

func (s *Service) openWorkspaceRemovalRoot() (*os.Root, error) {
	expected, err := s.workspaceParentPath()
	if err != nil {
		return nil, err
	}
	root, err := openVerifiedRoot(s.paths.WorkspacesDir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(s.paths.WorkspacesDir)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(expected) {
		return nil, closeRootsDenied("workspace parent changed while it was opened", err, root)
	}
	return root, nil
}

// openAcceptedParentRoot opens <DataDir>/workspaces through a verified DataDir
// root, so neither directory can be a symlink or swapped while it is opened.
func (s *Service) openAcceptedParentRoot() (*os.Root, error) {
	if s.paths.DataDir == "" {
		return nil, NewError(ErrPathDenied, "workspace data directory is not configured")
	}
	dataRoot, err := openVerifiedRoot(s.paths.DataDir)
	if err != nil {
		return nil, err
	}
	before, err := dataRoot.Lstat("workspaces")
	if err != nil || !isRealDir(before) {
		return nil, closeRootsDenied("accepted configuration parent is not a real directory", err, dataRoot)
	}
	root, err := dataRoot.OpenRoot("workspaces")
	if err != nil {
		return nil, closeRootsDenied("accepted configuration parent could not be opened safely", err, dataRoot)
	}
	opened, openedErr := root.Stat(".")
	after, afterErr := dataRoot.Lstat("workspaces")
	if openedErr != nil || afterErr != nil || !isRealDir(after) || !os.SameFile(before, after) || !os.SameFile(opened, after) {
		return nil, closeRootsDenied("accepted configuration parent changed while it was opened", errors.Join(openedErr, afterErr), root, dataRoot)
	}
	if err := dataRoot.Close(); err != nil {
		return nil, closeRootsDenied("workspace data directory could not be closed", err, root)
	}
	return root, nil
}

// openVerifiedRoot opens path as an os.Root and checks that path is a real
// directory that is still the one that was opened.
func openVerifiedRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil || !isRealDir(before) {
		return nil, WrapError(ErrPathDenied, "directory is not a real directory", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, WrapError(ErrPathDenied, "directory could not be opened safely", err)
	}
	opened, openedErr := root.Stat(".")
	after, afterErr := os.Lstat(path)
	if openedErr != nil || afterErr != nil || !isRealDir(after) || !os.SameFile(opened, after) {
		return nil, closeRootsDenied("directory changed while it was opened", errors.Join(openedErr, afterErr), root)
	}
	return root, nil
}

func isRealDir(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink == 0 && info.IsDir()
}

// closeRootsDenied closes roots after a failure. A cause that already carries a
// workspace code keeps it; any other cause becomes ErrPathDenied.
func closeRootsDenied(message string, cause error, roots ...*os.Root) error {
	code := ErrPathDenied
	var closeErr error
	for _, root := range roots {
		closeErr = errors.Join(closeErr, root.Close())
	}
	if inner := CodeOf(cause); inner != "" {
		if closeErr == nil {
			return cause
		}
		code = inner
	}
	return WrapError(code, message, errors.Join(cause, closeErr))
}

func (s *Service) validateWorkspaceDirectory(id string) error {
	workspaceDir := s.paths.WorkspaceDir(id)
	info, err := os.Lstat(workspaceDir)
	if os.IsNotExist(err) {
		return NewError(ErrNotFound, "workspace was not found")
	}
	if err != nil || !isRealDir(info) {
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
	if err != nil || !isRealDir(parentInfo) {
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
