package workspace

import (
	"bytes"
	"context"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// AddRoot adds a validated named root through the config CAS write path.
func (s *Service) AddRoot(ctx context.Context, id, expectedHash string, spec RootSpec) (*Workspace, error) {
	return s.mutateRoots(ctx, id, expectedHash, func(roots []Root, home string) ([]Root, error) {
		root, err := s.validateRoot(spec.Name, spec.Access, spec.Path, home, roots)
		if err != nil {
			return nil, err
		}
		return append(roots, root), nil
	})
}

// RemoveRoot removes one named root through the config CAS write path.
func (s *Service) RemoveRoot(ctx context.Context, id, expectedHash, name string) (*Workspace, error) {
	return s.mutateRoots(ctx, id, expectedHash, func(roots []Root, _ string) ([]Root, error) {
		for i, root := range roots {
			if root.Name == name {
				return append(roots[:i:i], roots[i+1:]...), nil
			}
		}
		return nil, NewError(ErrRootInvalid, "root was not found")
	})
}

// UpdateRoot renames or changes the path or access of a root through config CAS.
func (s *Service) UpdateRoot(ctx context.Context, id, expectedHash, name string, update RootSpec) (*Workspace, error) {
	return s.mutateRoots(ctx, id, expectedHash, func(roots []Root, home string) ([]Root, error) {
		idx := -1
		for i := range roots {
			if roots[i].Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, NewError(ErrRootInvalid, "root was not found")
		}
		current := roots[idx]
		if update.Name == "" {
			update.Name = current.Name
		}
		if update.Path == "" {
			update.Path = current.Path
		}
		if update.Access == "" {
			update.Access = current.Access
		}
		others := append(slices.Clone(roots[:idx]), roots[idx+1:]...)
		changed, err := s.validateRoot(update.Name, update.Access, update.Path, home, others)
		if err != nil {
			return nil, err
		}
		roots[idx] = changed
		return roots, nil
	})
}

func (s *Service) mutateRoots(ctx context.Context, id, expectedHash string, mutate func([]Root, string) ([]Root, error)) (*Workspace, error) {
	if expectedHash == "" {
		return nil, NewError(ErrConfigChanged, "expected_hash is required")
	}
	rec, home, raw, _, _, err := s.loadWorkspaceInputs(ctx, id)
	if err != nil {
		return nil, err
	}
	if hashErr := checkExpectedHash(raw, rec.ConfigHash, expectedHash); hashErr != nil {
		return nil, hashErr
	}
	cfg, roots, err := s.parseAndValidateTier3WithRoots(id, raw, home)
	if err != nil {
		return nil, err
	}
	newRoots, err := mutate(roots, home)
	if err != nil {
		return nil, err
	}
	cfg.Roots = &newRoots
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(cfg); err != nil {
		return nil, WrapError(ErrConfigInvalid, "encode workspace configuration", err)
	}
	return s.UpdateWorkspaceConfig(ctx, id, encoded.Bytes(), expectedHash)
}
