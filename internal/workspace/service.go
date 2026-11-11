package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// Workspace represents a fully resolved workspace.
type Workspace struct {
	ID, Name, Dir                  string
	Roots                          []Root
	ConfigRevision, PolicyRevision int64
	ConfigHash, AcceptedConfigHash string
	MissingRoots                   []string
	CreatedAt                      string
}

// CreateWorkspaceRequest contains the parameters for creating a new workspace.
type CreateWorkspaceRequest struct {
	Name    string
	Roots   []RootSpec
	HomeDir string // Injected user home dir for expanding ~ in roots (optional)
}

// ServiceOption configures optional behaviors of Service.
type ServiceOption func(*Service)

// WithStatUID sets a custom UID lookup function (used in tests).
func WithStatUID(fn func(os.FileInfo) (int, error)) ServiceOption {
	return func(s *Service) { s.statUID = fn }
}

// WithLogger sets a custom logger for permission audits.
func WithLogger(logger *slog.Logger) ServiceOption {
	return func(s *Service) { s.logger = logger }
}

// Service manages workspace lifecycle, directory layout, roots and persistence.
type Service struct {
	paths   Paths
	store   *Store
	clock   clock.Clock
	ids     ids.Generator
	statUID func(os.FileInfo) (int, error)
	logger  *slog.Logger
}

// NewService returns a new workspace Service.
func NewService(paths Paths, db *sql.DB, clk clock.Clock, gen ids.Generator, opts ...ServiceOption) *Service {
	s := &Service{
		paths: paths, store: NewStore(db), clock: clk, ids: gen,
		statUID: fileSysUID, logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Paths returns the paths configuration used by this service.
func (s *Service) Paths() Paths { return s.paths }

// CreateWorkspace creates a new workspace directory layout, minimal fugue.toml,
// read-only WORKSPACE.md, and database record.
func (s *Service) CreateWorkspace(ctx context.Context, req CreateWorkspaceRequest) (*Workspace, error) {
	if req.Name == "" {
		return nil, NewError(ErrRootInvalid, "workspace name cannot be empty")
	}

	id, err := s.ids.NewID()
	if err != nil {
		return nil, WrapError(ErrOwnershipInvalid, "generate workspace ID", err)
	}

	homeDir, homeErr := resolveHome(req.HomeDir)
	if homeErr != nil {
		return nil, homeErr
	}

	validatedRoots, rootErr := s.validateRoots(req.Roots, homeDir)
	if rootErr != nil {
		return nil, rootErr
	}

	wsDir := s.paths.WorkspaceDir(id)
	tomlContent, layoutErr := s.createWorkspaceLayout(ctx, wsDir, id, req.Name, validatedRoots)
	if layoutErr != nil {
		return nil, layoutErr
	}

	hash := computeConfigHash([]byte(tomlContent))
	ws := &Workspace{
		ID: id, Name: req.Name, Roots: validatedRoots,
		ConfigRevision: 1, PolicyRevision: 1,
		ConfigHash: hash, AcceptedConfigHash: hash,
		MissingRoots: []string{}, CreatedAt: s.clock.Now().UTC().Format(time.RFC3339Nano),
		Dir: wsDir,
	}
	rec := Record{
		WorkspaceID: ws.ID, ConfigRevision: 1, PolicyRevision: 1,
		ConfigHash: hash, AcceptedConfigHash: hash,
		MissingRoots: ws.MissingRoots, CreatedAt: ws.CreatedAt,
	}
	if storeErr := s.store.Create(ctx, &rec); storeErr != nil {
		return nil, storeErr
	}
	return ws, nil
}

func (s *Service) createWorkspaceLayout(
	ctx context.Context,
	wsDir, id, name string,
	roots []Root,
) (string, error) {
	for _, dir := range []string{wsDir, filepath.Join(wsDir, "scratch")} {
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return "", WrapError(ErrOwnershipInvalid, "create dir "+dir, err)
		}
	}

	tomlContent := formatMinimalTOML(name, roots)
	if err := writeAtomic(wsDir, "fugue.toml", []byte(tomlContent)); err != nil {
		return "", err
	}
	if err := writeAtomic(wsDir, "WORKSPACE.md", []byte(GenerateWorkspaceMD(id, name, roots))); err != nil {
		return "", err
	}
	if err := CheckAndEnforcePermissions(ctx, wsDir, s.statUID, s.logger); err != nil {
		return "", err
	}
	return tomlContent, nil
}

func resolveHome(injected string) (string, error) {
	if injected != "" {
		return injected, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", WrapError(ErrPathDenied, "resolve user home directory", err)
	}
	return home, nil
}

func (s *Service) validateRoots(specs []RootSpec, homeDir string) ([]Root, error) {
	roots := make([]Root, 0, len(specs))
	for _, spec := range specs {
		if err := ValidateRootName(spec.Name); err != nil {
			return nil, err
		}
		if err := ValidateRootAccess(spec.Access); err != nil {
			return nil, err
		}
		canonPath, err := ValidateAndResolveRootPath(spec.Path, homeDir, s.paths.FugueHome, s.paths.DataDir)
		if err != nil {
			return nil, err
		}
		newRoot := Root{Name: spec.Name, Path: canonPath, Access: spec.Access}
		if err := CheckRootOverlap(roots, newRoot); err != nil {
			return nil, err
		}
		roots = append(roots, newRoot)
	}
	return roots, nil
}

// GetWorkspace loads a workspace, enforcing file permissions and detecting missing roots.
func (s *Service) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	wsDir := s.paths.WorkspaceDir(id)
	if checkErr := CheckAndEnforcePermissions(ctx, wsDir, s.statUID, s.logger); checkErr != nil {
		return nil, checkErr
	}

	cfgData, readErr := os.ReadFile(s.paths.ConfigPath(id))
	if readErr != nil {
		return nil, WrapError(ErrNotFound, "read config for workspace "+id, readErr)
	}
	cfg, parseErr := parseMinimalTOML(cfgData)
	if parseErr != nil {
		return nil, parseErr
	}

	missing := DetectMissingRoots(cfg.Roots)
	if !slices.Equal(missing, rec.MissingRoots) {
		if updateErr := s.store.UpdateMissingRoots(ctx, id, missing); updateErr != nil {
			return nil, updateErr
		}
		rec.MissingRoots = missing
	}

	return &Workspace{
		ID: rec.WorkspaceID, Name: cfg.Name, Roots: cfg.Roots,
		ConfigRevision: rec.ConfigRevision, PolicyRevision: rec.PolicyRevision,
		ConfigHash: rec.ConfigHash, AcceptedConfigHash: rec.AcceptedConfigHash,
		MissingRoots: rec.MissingRoots, CreatedAt: rec.CreatedAt, Dir: wsDir,
	}, nil
}

// ListWorkspaces returns summaries of all workspaces.
func (s *Service) ListWorkspaces(ctx context.Context) ([]*Workspace, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	workspaces := make([]*Workspace, 0, len(records))
	for _, rec := range records {
		ws, getErr := s.GetWorkspace(ctx, rec.WorkspaceID)
		if getErr != nil {
			return nil, getErr
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

// SelectSessionCwd selects and returns the canonical real path of ONE validated root
// for a vendor session (Q-57, Q-65). If rootName is empty and the workspace has
// exactly one root, that root is selected. Fails with workspace.root_missing if
// the root is not present or valid on disk.
func (s *Service) SelectSessionCwd(ctx context.Context, workspaceID, rootName string) (string, error) {
	ws, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", err
	}

	targetName, resolveErr := resolveTargetRootName(ws.Roots, rootName)
	if resolveErr != nil {
		return "", resolveErr
	}

	idx := slices.IndexFunc(ws.Roots, func(r Root) bool { return r.Name == targetName })
	if idx == -1 {
		return "", NewError(ErrRootInvalid, fmt.Sprintf("root %q not found in workspace", targetName))
	}
	targetRoot := ws.Roots[idx]

	if IsRootMissing(targetRoot) {
		missing := DetectMissingRoots(ws.Roots)
		if !slices.Contains(missing, targetRoot.Name) {
			missing = append(missing, targetRoot.Name)
		}
		if updateErr := s.store.UpdateMissingRoots(ctx, workspaceID, missing); updateErr != nil {
			return "", updateErr
		}
		return "", NewError(
			ErrRootMissing,
			fmt.Sprintf("root %q is missing on disk at %s", targetRoot.Name, targetRoot.Path),
		)
	}

	return targetRoot.Path, nil
}

func resolveTargetRootName(roots []Root, rootName string) (string, error) {
	if rootName != "" {
		return rootName, nil
	}
	switch len(roots) {
	case 0:
		return "", NewError(ErrRootMissing, "workspace has no configured roots")
	case 1:
		return roots[0].Name, nil
	default:
		return "", NewError(ErrRootInvalid, "workspace has multiple roots; root name must be specified")
	}
}
