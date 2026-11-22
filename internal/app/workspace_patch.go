package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	workspacePatchDedupCap    = 128
	maxWorkspaceConfigPatches = 64
)

// WorkspaceConfigPatchOp identifies the operation for a structured workspace config patch.
type WorkspaceConfigPatchOp uint8

// Supported structured workspace config patch operations.
const (
	WorkspacePatchSet WorkspaceConfigPatchOp = iota + 1
	WorkspacePatchDelete
)

// WorkspaceConfigPatch describes one field-level update or deletion at the app boundary.
type WorkspaceConfigPatch struct {
	DottedKey string
	Op        WorkspaceConfigPatchOp
	Value     any
}

type workspacePatchDedupEntry struct {
	fingerprint string
	view        *WorkspaceView
}

type workspacePatchDedup struct {
	mu      sync.Mutex
	entries map[string]workspacePatchDedupEntry
	order   []string
}

func newWorkspacePatchDedup() *workspacePatchDedup {
	return &workspacePatchDedup{entries: make(map[string]workspacePatchDedupEntry)}
}

func (d *workspacePatchDedup) lookup(commandID, fp string) (*WorkspaceView, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.entries[commandID]
	if !ok {
		return nil, false, nil
	}
	if e.fingerprint != fp {
		return nil, false, errcode.New(errcode.InvalidArgument, "command_id was already used for a different command")
	}
	return e.view, true, nil
}

func (d *workspacePatchDedup) store(commandID, fp string, view *WorkspaceView) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.entries[commandID]; exists {
		return
	}
	if len(d.order) >= workspacePatchDedupCap {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.entries, oldest)
	}
	d.entries[commandID] = workspacePatchDedupEntry{fingerprint: fp, view: view}
	d.order = append(d.order, commandID)
}

// PatchWorkspaceConfig serves the PatchWorkspaceConfig Host API RPC.
func (rt *HostRuntime) PatchWorkspaceConfig(ctx context.Context, principal, commandID, id, expectedHash string, patches []WorkspaceConfigPatch) (*WorkspaceView, error) {
	return rt.workspaces.Patch(ctx, principal, commandID, id, expectedHash, patches)
}

// Patch applies structured field-level patches to a workspace's fugue.toml while
// preserving comments and key order, gated to the local app principal with
// command_id deduplication and CAS on expectedHash.
func (s *WorkspaceService) Patch(ctx context.Context, principal, commandID, id, expectedHash string, patches []WorkspaceConfigPatch) (*WorkspaceView, error) {
	if !IsAppPrincipal(principal) {
		return nil, errcode.New(errcode.PermissionDenied, "only the app client may patch workspace configuration")
	}
	if commandID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if len(patches) == 0 || len(patches) > maxWorkspaceConfigPatches {
		return nil, errcode.New(errcode.InvalidArgument, "config patch count must be between 1 and 64")
	}
	fp, err := workspacePatchFingerprint(id, expectedHash, patches)
	if err != nil {
		return nil, err
	}
	if cached, hit, lookupErr := s.patchDedup.lookup(commandID, fp); lookupErr != nil {
		return nil, lookupErr
	} else if hit {
		return cached, nil
	}
	domainPatches, err := toDomainConfigPatches(patches)
	if err != nil {
		return nil, err
	}
	ws, err := s.domain.PatchWorkspaceConfig(ctx, id, expectedHash, domainPatches)
	if err != nil {
		return nil, publicWorkspaceError(err)
	}
	s.publish(EventWorkspaceConfigChanged, ws)
	view := workspaceView(ws)
	s.patchDedup.store(commandID, fp, view)
	return view, nil
}

func toDomainConfigPatches(patches []WorkspaceConfigPatch) ([]workspace.ConfigPatch, error) {
	out := make([]workspace.ConfigPatch, 0, len(patches))
	for _, p := range patches {
		var op workspace.ConfigPatchOp
		switch p.Op {
		case WorkspacePatchSet:
			op = workspace.PatchOpSet
		case WorkspacePatchDelete:
			op = workspace.PatchOpDelete
		default:
			return nil, errcode.New(errcode.InvalidArgument, "unsupported config patch operation")
		}
		out = append(out, workspace.ConfigPatch{
			DottedKey: p.DottedKey,
			Op:        op,
			Value:     p.Value,
		})
	}
	return out, nil
}

func workspacePatchFingerprint(id, expectedHash string, patches []WorkspaceConfigPatch) (string, error) {
	payload, err := json.Marshal(patches)
	if err != nil {
		return "", errcode.Wrap(errcode.InvalidArgument, "encode config patches", err)
	}
	h := sha256.New()
	h.Write([]byte(id))
	h.Write([]byte{0})
	h.Write([]byte(expectedHash))
	h.Write([]byte{0})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}
