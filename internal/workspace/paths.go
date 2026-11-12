package workspace

import (
	"fmt"
	"os"
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
	workspacesDirOverride string
}

// WithWorkspacesDir sets an explicit workspaces directory override.
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
	} else if dir, cfgErr := resolveGlobalWorkspacesDir(Paths{FugueHome: fugueHome}, userHomeDir); cfgErr != nil {
		return Paths{}, cfgErr
	} else if dir != "" {
		workspacesDir = dir
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

// GlobalConfigPath returns the path to the Tier 2 global config (<FugueHome>/config.toml).
func (p Paths) GlobalConfigPath() string { return filepath.Join(p.FugueHome, "config.toml") }

// AcceptedGlobalConfigPath returns the path to the Tier 2 accepted copy (<DataDir>/accepted-config.toml).
func (p Paths) AcceptedGlobalConfigPath() string {
	return filepath.Join(p.DataDir, "accepted-config.toml")
}

// AcceptedWorkspaceDir returns the directory in DataDir holding the Tier 3 accepted copy.
func (p Paths) AcceptedWorkspaceDir(id string) string {
	if p.DataDir != "" {
		return filepath.Join(p.DataDir, "workspaces", id)
	}
	return p.WorkspaceDir(id)
}

// AcceptedConfigPath returns the path to the Tier 3 accepted copy (<DataDir>/workspaces/<id>/accepted.toml).
func (p Paths) AcceptedConfigPath(id string) string {
	return filepath.Join(p.AcceptedWorkspaceDir(id), "accepted.toml")
}

func resolveGlobalWorkspacesDir(p Paths, userHomeDir func() (string, error)) (string, error) {
	raw, err := os.ReadFile(p.GlobalConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", WrapError(ErrConfigInvalid, "read config.toml", err)
	}
	var home string
	if userHomeDir != nil {
		if h, hErr := userHomeDir(); hErr == nil {
			home = h
		}
	}
	cfg, decErr := decodeTier2TOML(p.GlobalConfigPath(), raw, home)
	if decErr != nil || cfg.Host == nil || cfg.Host.WorkspacesDir == nil || *cfg.Host.WorkspacesDir == "" {
		return "", decErr
	}
	return filepath.Clean(expandHome(*cfg.Host.WorkspacesDir, home)), nil
}
