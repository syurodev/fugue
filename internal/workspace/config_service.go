package workspace

import (
	"context"
	"os"
	"slices"
)

// GetEffectiveConfig merges all four configuration tiers for workspace id (WS-05, Q-64).
func (s *Service) GetEffectiveConfig(ctx context.Context, id string, override *SessionOverride) (EffectiveConfig, *Workspace, error) {
	if override != nil {
		if err := firstErr(errIf(override.Effort != "" && !isValidEffort(override.Effort), "session", "effort", "effort must be low, medium, high, xhigh, or max"), checkNoSecret("session", "model", override.Model)); err != nil {
			return EffectiveConfig{}, nil, err
		}
	}
	ws, eff, err := s.loadWorkspaceAndConfig(ctx, id, override)
	return eff, ws, err
}

// UpdateWorkspaceConfig validates and atomically updates fugue.toml and accepted.toml with CAS verification (WS-E09).
func (s *Service) UpdateWorkspaceConfig(ctx context.Context, id string, rawTOML []byte, expectedHash string) (*Workspace, error) {
	return s.applyWorkspaceConfig(ctx, id, rawTOML, expectedHash, false)
}

// ConfirmWorkspaceConfig confirms a pending out-of-band configuration change (Q-64, Q-70: write accepted.toml first, update DB second).
func (s *Service) ConfirmWorkspaceConfig(ctx context.Context, id, expectedHash string) (*Workspace, error) {
	return s.applyWorkspaceConfig(ctx, id, nil, expectedHash, true)
}

func (s *Service) applyWorkspaceConfig(ctx context.Context, id string, rawTOML []byte, expectedHash string, isConfirm bool) (*Workspace, error) {
	rec, homeDir, diskBytes, accCfg, t2Cfg, err := s.loadWorkspaceInputs(ctx, id)
	if err != nil {
		return nil, err
	}
	if isConfirm {
		if err := checkExpectedHash(diskBytes, rec.ConfigHash, expectedHash); err != nil {
			return nil, err
		}
		rawTOML = diskBytes
	} else if err := checkExpectedHash(diskBytes, rec.ConfigHash, expectedHash); err != nil {
		return nil, err
	}
	newCfg, roots, valErr := s.parseAndValidateTier3WithRoots(id, rawTOML, homeDir)
	if valErr != nil {
		return nil, valErr
	}
	base := DefaultConfig(s.paths.FugueHome, id)
	oldEff, newEff := mergeConfigs(&base, &t2Cfg, &accCfg, nil, homeDir), mergeConfigs(&base, &t2Cfg, &newCfg, nil, homeDir)
	if !isConfirm {
		if err := writeAtomic(s.paths.WorkspaceDir(id), "fugue.toml", rawTOML); err != nil {
			return nil, err
		}
	}
	return s.commitAcceptedConfig(ctx, id, rec, rawTOML, newEff.Name, roots, len(diffPolicy(&oldEff, &newEff)) > 0, isConfirm)
}

// RejectWorkspaceConfig restores fugue.toml atomically from accepted.toml without bumping policy_revision (Q-64, Q-70).
func (s *Service) RejectWorkspaceConfig(ctx context.Context, id, expectedHash string) (*Workspace, error) {
	rec, homeDir, err := s.prepareWorkspaceOp(ctx, id)
	if err != nil {
		return nil, err
	}
	accBytes, _, accErr := s.loadAcceptedCopy(ctx, id, rec.AcceptedConfigHash, homeDir)
	if accErr != nil {
		return nil, accErr
	}
	diskBytes, readErr := os.ReadFile(s.paths.ConfigPath(id))
	if readErr != nil {
		return nil, WrapError(ErrNotFound, "read config for workspace", readErr)
	}
	if err := checkExpectedHash(diskBytes, rec.ConfigHash, expectedHash); err != nil {
		return nil, err
	}
	rec.ConfigHash = rec.AcceptedConfigHash
	if err := firstErr(writeAtomic(s.paths.WorkspaceDir(id), "fugue.toml", accBytes), s.store.Update(ctx, rec)); err != nil {
		return nil, err
	}
	return s.GetWorkspace(ctx, id)
}

func checkExpectedHash(diskBytes []byte, recHash, expectedHash string) error {
	if expectedHash == "" || computeConfigHash(diskBytes) != expectedHash || recHash != expectedHash {
		return NewError(ErrConfigChanged, "workspace configuration hash does not match expected_hash")
	}
	return nil
}

func (s *Service) commitAcceptedConfig(
	ctx context.Context, id string, rec *Record, rawTOML []byte,
	name string, roots []Root, bumpPolicy, runCrashHook bool,
) (*Workspace, error) {
	if err := s.writeAcceptedCopy(ctx, id, rawTOML); err != nil { // Step 1 (Q-70): write accepted copy FIRST.
		return nil, err
	}
	if runCrashHook && s.beforeStoreUpdate != nil {
		if err := s.beforeStoreUpdate(ctx, id); err != nil {
			return nil, err
		}
	}
	newHash := computeConfigHash(rawTOML) // Step 2 (Q-70): update database hashes and revisions SECOND.
	rec.ConfigRevision++
	rec.ConfigHash, rec.AcceptedConfigHash = newHash, newHash
	if bumpPolicy {
		rec.PolicyRevision++
	}
	if err := firstErr(s.store.Update(ctx, rec), writeAtomic(s.paths.WorkspaceDir(id), "WORKSPACE.md", []byte(GenerateWorkspaceMD(id, name, roots)))); err != nil {
		return nil, err
	}
	if bumpPolicy && s.grantInvalidator != nil {
		s.grantInvalidator.InvalidateWorkspaceGrants(id)
	}
	return s.GetWorkspace(ctx, id)
}

func (s *Service) prepareWorkspaceOp(ctx context.Context, id string) (*Record, string, error) {
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	homeDir, homeErr := s.userHome("")
	return rec, homeDir, firstErr(CheckAndEnforcePermissions(ctx, s.paths, id, s.statUID, s.logger), homeErr)
}

func (s *Service) parseAndValidateTier3WithRoots(id string, rawTOML []byte, homeDir string) (tier3RawConfig, []Root, error) {
	cfg, err := decodeTier3TOML(s.paths.ConfigPath(id), rawTOML, homeDir)
	if err != nil {
		return tier3RawConfig{}, nil, err
	}
	var specs []RootSpec
	for _, r := range derefSlice(cfg.Roots) {
		specs = append(specs, RootSpec(r))
	}
	roots, rootErr := s.validateRoots(specs, homeDir)
	cfg.Roots = &roots
	return cfg, roots, rootErr
}

func (s *Service) loadWorkspaceInputs(ctx context.Context, id string) (*Record, string, []byte, tier3RawConfig, tier2RawConfig, error) {
	rec, homeDir, err := s.prepareWorkspaceOp(ctx, id)
	if err != nil {
		return nil, "", nil, tier3RawConfig{}, tier2RawConfig{}, err
	}
	_, accCfg, accErr := s.loadAcceptedCopy(ctx, id, rec.AcceptedConfigHash, homeDir)
	if accErr != nil {
		return nil, "", nil, tier3RawConfig{}, tier2RawConfig{}, accErr
	}
	t2Cfg, t2Err := s.loadTier2Config(ctx, homeDir)
	if t2Err != nil {
		return nil, "", nil, tier3RawConfig{}, tier2RawConfig{}, t2Err
	}
	diskBytes, readErr := os.ReadFile(s.paths.ConfigPath(id))
	if readErr != nil {
		return nil, "", nil, tier3RawConfig{}, tier2RawConfig{}, WrapError(ErrNotFound, "read config for workspace "+id, readErr)
	}
	return rec, homeDir, diskBytes, accCfg, t2Cfg, nil
}

func (s *Service) loadWorkspaceAndConfig(ctx context.Context, id string, override *SessionOverride) (*Workspace, EffectiveConfig, error) {
	rec, homeDir, diskBytes, accCfg, t2Cfg, err := s.loadWorkspaceInputs(ctx, id)
	if err != nil {
		return nil, EffectiveConfig{}, err
	}
	baseDefault, diskHash := DefaultConfig(s.paths.FugueHome, id), computeConfigHash(diskBytes)
	activeCfg, pending, diffs, cfgErr, syncErr := s.reconcileOutOfBand(ctx, id, rec, diskBytes, diskHash, accCfg, &baseDefault, &t2Cfg, homeDir)
	if syncErr != nil {
		return nil, EffectiveConfig{}, syncErr
	}
	eff := mergeConfigs(&baseDefault, &t2Cfg, &activeCfg, override, homeDir)
	ws, buildErr := s.buildWorkspace(ctx, id, rec, &eff, diskHash, pending, diffs, cfgErr, homeDir)
	return ws, eff, buildErr
}

func (s *Service) reconcileOutOfBand(
	ctx context.Context, id string, rec *Record, diskBytes []byte, diskHash string,
	accCfg tier3RawConfig, baseDefault *EffectiveConfig, t2Cfg *tier2RawConfig, homeDir string,
) (tier3RawConfig, bool, []PolicyDiffEntry, error, error) {
	diskCfg, parseErr := decodeTier3TOML(s.paths.ConfigPath(id), diskBytes, homeDir)
	if parseErr != nil {
		return accCfg, false, nil, parseErr, nil
	}
	if diskHash == rec.AcceptedConfigHash {
		return diskCfg, false, nil, nil, nil
	}
	candEff, accNoOverride := mergeConfigs(baseDefault, t2Cfg, &diskCfg, nil, homeDir), mergeConfigs(baseDefault, t2Cfg, &accCfg, nil, homeDir)
	if diffs := diffPolicy(&accNoOverride, &candEff); len(diffs) > 0 {
		if rec.ConfigHash != diskHash {
			rec.ConfigHash = diskHash
			if err := s.store.Update(ctx, rec); err != nil {
				return tier3RawConfig{}, false, nil, nil, err
			}
		}
		return accCfg, true, diffs, nil, nil
	}
	rec.ConfigRevision++
	rec.ConfigHash, rec.AcceptedConfigHash = diskHash, diskHash
	return diskCfg, false, nil, nil, firstErr(s.writeAcceptedCopy(ctx, id, diskBytes), s.store.Update(ctx, rec))
}

func (s *Service) buildWorkspace(
	ctx context.Context, id string, rec *Record, eff *EffectiveConfig,
	diskHash string, pending bool, diffs []PolicyDiffEntry, cfgErr error, homeDir string,
) (*Workspace, error) {
	ws := &Workspace{
		ID: rec.WorkspaceID, Name: eff.Name,
		ConfigRevision: rec.ConfigRevision, PolicyRevision: rec.PolicyRevision,
		ConfigHash: diskHash, AcceptedConfigHash: rec.AcceptedConfigHash,
		MissingRoots: rec.MissingRoots, InvalidRoots: make(map[string]error),
		PendingPolicyConfirmation: pending, PolicyDiff: diffs, ConfigError: cfgErr,
		CreatedAt: rec.CreatedAt, Dir: s.paths.WorkspaceDir(id),
	}
	validRoots := s.revalidateRoots(eff.Roots, homeDir, ws)
	ws.Roots = slices.Clone(eff.Roots)
	for i, r := range ws.Roots {
		if idx := slices.IndexFunc(validRoots, func(v Root) bool { return v.Name == r.Name }); idx != -1 {
			ws.Roots[i].Path = validRoots[idx].Path
		}
	}
	if missing := DetectMissingRoots(validRoots); !slices.Equal(missing, rec.MissingRoots) {
		ws.MissingRoots = missing
		return ws, s.store.UpdateMissingRoots(ctx, id, missing)
	}
	return ws, nil
}

func (s *Service) writeAcceptedCopy(ctx context.Context, id string, content []byte) error {
	accDir := s.paths.AcceptedWorkspaceDir(id)
	if err := os.MkdirAll(accDir, dirPerm); err != nil {
		return WrapError(ErrOwnershipInvalid, "create accepted config dir "+accDir, err)
	}
	return firstErr(checkEntry(ctx, pathEntry{path: accDir}, s.statUID, s.logger), writeAtomic(accDir, "accepted.toml", content), checkEntry(ctx, pathEntry{path: s.paths.AcceptedConfigPath(id)}, s.statUID, s.logger))
}

func (s *Service) statAndCheck(ctx context.Context, path string, missingErr error) error {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return missingErr
		}
		return WrapError(ErrOwnershipInvalid, "lstat "+path, err)
	}
	return checkEntry(ctx, pathEntry{path: path}, s.statUID, s.logger)
}

func (s *Service) loadAcceptedCopy(ctx context.Context, id, expectedHash, homeDir string) ([]byte, tier3RawConfig, error) {
	accDir, accPath := s.paths.AcceptedWorkspaceDir(id), s.paths.AcceptedConfigPath(id)
	if expectedHash == "" {
		return nil, tier3RawConfig{}, NewError(ErrConfigUnconfirmed, "workspace has no accepted_config_hash")
	}
	if err := firstErr(s.statAndCheck(ctx, accPath, NewError(ErrConfigUnconfirmed, "accepted configuration copy is missing for workspace "+id)), checkEntry(ctx, pathEntry{path: accDir}, s.statUID, s.logger)); err != nil {
		return nil, tier3RawConfig{}, err
	}
	raw, err := os.ReadFile(s.paths.AcceptedConfigPath(id))
	if err != nil || computeConfigHash(raw) != expectedHash {
		return nil, tier3RawConfig{}, NewError(ErrConfigUnconfirmed, "accepted configuration copy hash does not match accepted_config_hash")
	}
	cfg, decErr := decodeTier3TOML(accPath, raw, homeDir)
	return raw, cfg, decErr
}

func (s *Service) loadTier2Config(ctx context.Context, homeDir string) (tier2RawConfig, error) {
	globalPath := s.paths.GlobalConfigPath()
	if _, err := os.Lstat(globalPath); os.IsNotExist(err) {
		return tier2RawConfig{}, nil
	}
	if err := s.statAndCheck(ctx, globalPath, nil); err != nil {
		return tier2RawConfig{}, err
	}
	raw, err := os.ReadFile(s.paths.GlobalConfigPath())
	if err != nil {
		return tier2RawConfig{}, WrapError(ErrConfigInvalid, "read "+globalPath, err)
	}
	return decodeTier2TOML(globalPath, raw, homeDir)
}
