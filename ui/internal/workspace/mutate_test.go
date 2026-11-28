package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

// recordingClient records which client call each mutation reached and
// answers with a fixed failure, so a test pins the mutation-to-RPC routing
// without a real connection. A create or add answers with the workspace the
// next test in the chain expects.
// The recorded call names the routing test asserts on.
const (
	callCreate  = "create"
	callAddRoot = "add-root"
)

type recordingClient struct {
	calls   []string
	err     error
	created client.Workspace
	added   client.Workspace
}

func (r *recordingClient) CreateWorkspace(context.Context, string) (client.Workspace, error) {
	r.calls = append(r.calls, callCreate)
	return r.created, r.err
}

func (r *recordingClient) AddRoot(context.Context, string, string, string, string, string) (client.Workspace, error) {
	r.calls = append(r.calls, callAddRoot)
	return r.added, r.err
}

func (r *recordingClient) RemoveRoot(context.Context, string, string, string) error {
	r.calls = append(r.calls, "remove-root")
	return r.err
}

func (r *recordingClient) UpdateRoot(context.Context, string, string, string, string, string, string) error {
	r.calls = append(r.calls, "update-root")
	return r.err
}

func (r *recordingClient) DeleteWorkspace(context.Context, string, string, string) error {
	r.calls = append(r.calls, "delete")
	return r.err
}

func (r *recordingClient) ConfirmWorkspaceConfig(context.Context, string, string) error {
	r.calls = append(r.calls, "confirm")
	return r.err
}

func (r *recordingClient) RejectWorkspaceConfig(context.Context, string, string) error {
	r.calls = append(r.calls, "reject")
	return r.err
}

// TestMutatorRejectsEmptyExpectedHash pins that every compare-and-swap
// mutation refuses to send an empty hash to the host client.
func TestMutatorRejectsEmptyExpectedHash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind MutKind
	}{
		{"empty-add-root", MutAddRoot},
		{"empty-remove-root", MutRemoveRoot},
		{"empty-update-root", MutUpdateRoot},
		{"empty-delete", MutDelete},
		{"empty-confirm", MutConfirmPolicy},
		{"empty-reject", MutRejectPolicy},
	} {
		for _, hash := range []string{"", "   "} {
			t.Run(tc.name+"-"+hash, func(t *testing.T) {
				t.Parallel()
				rec := &recordingClient{}
				m := NewMutator(func() MutatorClient { return rec })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				m.Start(ctx)
				defer m.Stop()

				m.Do(&Mutation{Kind: tc.kind, WorkspaceID: "w", Name: "r", Path: "/p", Access: "rw", ConfirmName: "ws", ExpectedHash: hash})
				res := waitResult(t, m)
				if !errors.Is(res.Err, errEmptyExpectedHash) {
					t.Fatalf("empty-hash error = %v, want errEmptyExpectedHash", res.Err)
				}
				if len(rec.calls) != 0 {
					t.Fatalf("client calls = %v, want none when hash is empty", rec.calls)
				}
			})
		}
	}
}

// The hashes the chain test scripts.
const (
	chainCreatedHash = "hash-created"
	chainAddedHash   = "hash-added"
)

// waitResult takes the mutator's next result. The receive blocks: Start, Do
// and deliver are deterministic, and the package timeout catches a stuck
// goroutine, so no wall-clock wait is needed.
func waitResult(t *testing.T, m *Mutator) MutResult {
	t.Helper()
	return <-m.Results()
}

// TestMutatorPostsEachKindToItsClientCall pins the mutation-to-RPC wiring:
// every kind reaches the client call it names, with its values.
func TestMutatorPostsEachKindToItsClientCall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mut  Mutation
		call string
	}{
		{"create", Mutation{Kind: MutCreate, CreateName: "ws"}, "create"},
		{"add-root", Mutation{Kind: MutAddRoot, WorkspaceID: "w", Name: "r", Path: "/p", Access: "rw", ExpectedHash: "h"}, "add-root"},
		{"remove-root", Mutation{Kind: MutRemoveRoot, WorkspaceID: "w", Name: "r", ExpectedHash: "h"}, "remove-root"},
		{"update-root", Mutation{Kind: MutUpdateRoot, WorkspaceID: "w", Name: "r", NewName: "n", Path: "/p", Access: "ro", ExpectedHash: "h"}, "update-root"},
		{"delete", Mutation{Kind: MutDelete, WorkspaceID: "w", ExpectedHash: "h", ConfirmName: "name"}, "delete"},
		{"confirm", Mutation{Kind: MutConfirmPolicy, WorkspaceID: "w", ExpectedHash: "h"}, "confirm"},
		{"reject", Mutation{Kind: MutRejectPolicy, WorkspaceID: "w", ExpectedHash: "h"}, "reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingClient{err: errUnknownMutation}
			m := NewMutator(func() MutatorClient { return rec })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.Start(ctx)
			defer m.Stop()

			m.Do(&tc.mut)
			res := waitResult(t, m)
			if len(rec.calls) != 1 || rec.calls[0] != tc.call {
				t.Fatalf("client calls = %v, want [%s]", rec.calls, tc.call)
			}
			if res.Err == nil {
				t.Fatal("the fixed failure did not come back")
			}
			if res.Mutation != tc.mut {
				t.Fatalf("result mutation = %+v, want %+v", res.Mutation, tc.mut)
			}
		})
	}
}

// TestMutatorDropsRequestsWithoutConnection pins that a mutation posted while
// disconnected is dropped, not failed: the host never saw it, so a retry is
// the caller's move when it reconnects.
func TestMutatorDropsRequestsWithoutConnection(t *testing.T) {
	t.Parallel()
	m := NewMutator(func() MutatorClient { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	m.Do(&Mutation{Kind: MutCreate, CreateName: "ws"})
	// Stop synchronizes the goroutine's exit: a delivered result would be in
	// the buffered channel by now, so a non-blocking read proves the drop.
	m.Stop()
	select {
	case res := <-m.Results():
		t.Fatalf("a dropped request answered %+v, want silence", res)
	default:
	}
}

// TestMutatorQueueFullReportsAFailure pins that a request dropped because the
// queue is full comes back as a failed result, never a silent loss.
func TestMutatorQueueFullReportsAFailure(t *testing.T) {
	t.Parallel()
	m := NewMutator(func() MutatorClient { return nil })
	// No goroutine runs, so nothing drains the request queue.
	for range mutBuffer {
		m.Do(&Mutation{Kind: MutCreate, CreateName: "ws"})
	}
	m.Do(&Mutation{Kind: MutCreate, CreateName: "overflow"})
	select {
	case res := <-m.Results():
		if !errors.Is(res.Err, errMutatorQueueFull) {
			t.Fatalf("overflow error = %v, want errMutatorQueueFull", res.Err)
		}
		if res.Mutation.CreateName != "overflow" {
			t.Fatalf("overflow mutation = %+v, want the dropped one", res.Mutation)
		}
	default:
		t.Fatal("the overflow was lost silently")
	}
}

// TestMutatorChainsCreateAndAddRootHashes pins what the source-folder flow
// (FUG-115) chains on: a create's result carries the new workspace's id and
// config hash, and an add's result carries the hash after the add, so each
// compare-and-swap posts the hash the step before returned.
func TestMutatorChainsCreateAndAddRootHashes(t *testing.T) {
	t.Parallel()
	rec := &recordingClient{
		created: client.Workspace{ID: "w9", Name: "ws", ConfigHash: chainCreatedHash},
		added:   client.Workspace{ID: "w9", ConfigHash: chainAddedHash},
	}
	m := NewMutator(func() MutatorClient { return rec })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	m.Do(&Mutation{Kind: MutCreate, CreateName: "ws"})
	res := waitResult(t, m)
	if res.Err != nil {
		t.Fatalf("create failed: %v", res.Err)
	}
	if res.NewID != "w9" || res.NewHash != chainCreatedHash {
		t.Fatalf("create result = (id %q, hash %q), want the host's answer", res.NewID, res.NewHash)
	}

	m.Do(&Mutation{Kind: MutAddRoot, WorkspaceID: "w9", Name: "api", Path: "/repo/api", Access: "rw", ExpectedHash: chainCreatedHash})
	res = waitResult(t, m)
	if res.Err != nil {
		t.Fatalf("add failed: %v", res.Err)
	}
	if res.NewHash != chainAddedHash {
		t.Fatalf("add result hash = %q, want the hash after the add", res.NewHash)
	}
	if rec.calls[0] != callCreate || rec.calls[1] != callAddRoot {
		t.Fatalf("client calls = %v, want create then add-root", rec.calls)
	}
}
