package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/workspace"
	"github.com/syurodev/fugue/internal/workspacememory"
)

const (
	// TopicMemory is the Host API topic carrying workspace memory write notifications.
	TopicMemory = "memory"
	// EventMemoryEntryWritten is emitted when a workspace memory entry is written.
	EventMemoryEntryWritten = workspacememory.EventMemoryEntryWritten
)

type memorySecretScanner struct {
	detector *secrets.Detector
	extra    func(text string) []string
}

func newMemorySecretScanner(extra func(text string) []string) *memorySecretScanner {
	return &memorySecretScanner{
		detector: secrets.New(nil, secrets.Options{}),
		extra:    extra,
	}
}

func (s *memorySecretScanner) Scan(text string) []workspacememory.SecretFinding {
	found := s.detector.FindIn(text, "")
	var extraKinds []string
	if s.extra != nil {
		extraKinds = s.extra(text)
	}
	if len(found) == 0 && len(extraKinds) == 0 {
		return nil
	}
	out := make([]workspacememory.SecretFinding, 0, len(found)+len(extraKinds))
	for _, f := range found {
		out = append(out, workspacememory.SecretFinding{
			Kind: f.Kind,
			Key:  f.Key,
		})
	}
	for _, k := range extraKinds {
		out = append(out, workspacememory.SecretFinding{Kind: k})
	}
	return out
}

type memoryAnchorHasher struct {
	workspaces *workspace.Service
}

func (r *memoryAnchorHasher) HashAnchor(ctx context.Context, workspaceID, rootName, relPath string) (string, error) {
	if r.workspaces == nil {
		return "", errcode.New(errcode.FailedPrecondition, "workspace service is not configured")
	}
	digest, err := r.workspaces.HashFile(ctx, workspaceID, rootName, relPath)
	if err != nil {
		return "", publicWorkspaceError(err)
	}
	return digest, nil
}

type memoryEventSink struct {
	hub *EventHub
}

func (s *memoryEventSink) OnMemoryEntryWritten(_ context.Context, ev workspacememory.WrittenEvent) {
	if s.hub == nil {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("marshal memory entry written event", "error_code", string(errcode.CodeOf(err)))
		return
	}
	if _, err := s.hub.Publish(TopicMemory, Event{
		Type:    EventMemoryEntryWritten,
		Key:     ev.EntryID,
		Payload: payload,
	}); err != nil {
		slog.Warn("publish memory entry written event", "error_code", string(errcode.CodeOf(err)))
	}
}

func initWorkspaceMemory(hp *HostProcess, store *storage.Store, deps *HostDeps, workspaces *workspace.Service) error {
	if err := hp.rt.hub.RegisterEphemeral(TopicMemory, memoryTopicSnapshot, defaultEphemeralBufferCap); err != nil {
		if serr := store.Close(); serr != nil {
			return errcode.Wrap(errcode.Internal, "register the memory topic", errors.Join(err, serr))
		}
		return err
	}
	memStore, err := workspacememory.NewStore(
		store.DB(),
		deps.Clock,
		deps.IDs,
		newMemorySecretScanner(deps.TestMemorySecretKinds),
		&memoryAnchorHasher{workspaces: workspaces},
		&memoryEventSink{hub: hp.rt.hub},
		workspacememory.Config{},
	)
	if err != nil {
		if serr := store.Close(); serr != nil {
			return errcode.Wrap(errcode.Internal, "create workspace memory store", errors.Join(err, serr))
		}
		return fmt.Errorf("create workspace memory store: %w", err)
	}
	hp.rt.memory = memStore
	hp.rt.memoryService = newMemoryService(memStore, workspaces, deps.Clock, deps.IDs)
	return nil
}

func memoryTopicSnapshot(_ context.Context) ([]byte, error) {
	return []byte(`{"entries":[]}`), nil
}

// MemoryStore returns the workspace memory owner store wired into the host runtime.
func (rt *HostRuntime) MemoryStore() (*workspacememory.Store, error) {
	if rt.memory == nil {
		return nil, errcode.New(errcode.Unavailable, "the workspace memory store is unavailable")
	}
	return rt.memory, nil
}

// MemoryService returns the Host API application service for workspace memory.
func (rt *HostRuntime) MemoryService() (*MemoryService, error) {
	if rt.memoryService == nil {
		return nil, errcode.New(errcode.Unavailable, "the workspace memory service is unavailable")
	}
	return rt.memoryService, nil
}
