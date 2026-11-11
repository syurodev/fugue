package workspace

import (
	"fmt"
	"path/filepath"

	"github.com/syurodev/fugue/internal/config"
)

// Paths holds the resolved root directories for the Fugue workspace subsystem.
type Paths struct {
	FugueHome     string // User root for Fugue config and workspaces (default: ~/Fugue).
	WorkspacesDir string // Directory containing per-workspace directories (default: <FugueHome>/workspaces).
	DataDir       string // Independent data directory for db, sockets, vault (SYNX_FUGUE_DATA_DIR).
}

// PathOption configures path resolution options.
type PathOption func(*pathOptions)

type pathOptions struct {
	// workspacesDirOverride sets an explicit workspaces directory (junction point for F5.6b [host].workspaces_dir).
	workspacesDirOverride string
}

// WithWorkspacesDir sets an explicit workspaces directory override.
// Junction point for [host].workspaces_dir from F5.6b.
func WithWorkspacesDir(dir string) PathOption {
	return func(o *pathOptions) { o.workspacesDirOverride = dir }
}

// ResolvePaths resolves FugueHome, WorkspacesDir, and DataDir according to Q-60 A.
func ResolvePaths(env config.Env, userHomeDir func() (string, error), dataDir string, opts ...PathOption) (Paths, error) {
	var options pathOptions
	for _, opt := range opts {
		opt(&options)
	}

	fugueHome, err := resolveFugueHome(env, userHomeDir)
	if err != nil {
		return Paths{}, err
	}

	workspacesDir := filepath.Join(fugueHome, "workspaces")
	if options.workspacesDirOverride != "" {
		if !filepath.IsAbs(options.workspacesDirOverride) {
			return Paths{}, NewError(ErrPathDenied, "workspaces_dir must be an absolute path: "+options.workspacesDirOverride)
		}
		workspacesDir = filepath.Clean(options.workspacesDirOverride)
	}

	cleanDataDir := ""
	if dataDir != "" {
		if !filepath.IsAbs(dataDir) {
			return Paths{}, NewError(ErrPathDenied, "data directory must be an absolute path: "+dataDir)
		}
		cleanDataDir = filepath.Clean(dataDir)
	}

	return Paths{FugueHome: fugueHome, WorkspacesDir: workspacesDir, DataDir: cleanDataDir}, nil
}

func resolveFugueHome(env config.Env, userHomeDir func() (string, error)) (string, error) {
	if v, ok := env.Lookup(config.EnvFugueHome); ok && v != "" {
		if !filepath.IsAbs(v) {
			return "", NewError(ErrPathDenied, fmt.Sprintf("%s must be an absolute path: %s", config.EnvFugueHome, v))
		}
		return filepath.Clean(v), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", WrapError(ErrPathDenied, "resolve user home directory", err)
	}
	return filepath.Join(home, "Fugue"), nil
}

// WorkspaceDir returns the directory path for the given workspace ID.
func (p Paths) WorkspaceDir(id string) string { return filepath.Join(p.WorkspacesDir, id) }

// ScratchDir returns the scratch directory path for the given workspace ID.
func (p Paths) ScratchDir(id string) string { return filepath.Join(p.WorkspaceDir(id), "scratch") }

// ConfigPath returns the path to fugue.toml for the given workspace ID.
func (p Paths) ConfigPath(id string) string { return filepath.Join(p.WorkspaceDir(id), "fugue.toml") }

// WorkspaceMDPath returns the path to WORKSPACE.md for the given workspace ID.
func (p Paths) WorkspaceMDPath(id string) string {
	return filepath.Join(p.WorkspaceDir(id), "WORKSPACE.md")
}
