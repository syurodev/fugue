package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/pkg/client"
)

type fakeSecretsClient struct {
	listRes  []client.SecretMeta
	listErr  error
	putErr   error
	delErr   error
	puts     []client.PutSecretInput
	captured [][]byte
	deletes  []struct{ cmdID, secretID string }
}

func (f *fakeSecretsClient) ListSecrets(_ context.Context, _, _ string) ([]client.SecretMeta, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listRes, nil
}

func (f *fakeSecretsClient) PutSecret(_ context.Context, in *client.PutSecretInput) (client.SecretMeta, error) {
	cp := *in
	cp.Value = bytes.Clone(in.Value)
	f.puts = append(f.puts, cp)
	f.captured = append(f.captured, in.Value)
	clear(in.Value)
	if f.putErr != nil {
		return client.SecretMeta{}, f.putErr
	}
	return client.SecretMeta{
		SecretID:    "sec-" + in.Name,
		ScopeKind:   in.ScopeKind,
		ScopeID:     in.ScopeID,
		Name:        in.Name,
		Description: in.Description,
		UpdatedAt:   time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (f *fakeSecretsClient) DeleteSecret(_ context.Context, commandID, secretID string) (bool, error) {
	f.deletes = append(f.deletes, struct{ cmdID, secretID string }{commandID, secretID})
	if f.delErr != nil {
		return false, f.delErr
	}
	return true, nil
}

func TestModelSnapshotEventsAndScopes(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if m.HasSnapshot() {
		t.Fatal("fresh model should not have snapshot")
	}
	snap, err := json.Marshal(client.SecretsSnapshot{
		Secrets: []client.SecretMeta{
			{SecretID: "s2", ScopeKind: client.SecretScopeWorkspace, ScopeID: "w1", Name: "DB_PASS"},
			{SecretID: "s1", ScopeKind: client.SecretScopeGlobal, Name: "ANTHROPIC_KEY"},
			{SecretID: "s3", ScopeKind: client.SecretScopeWorkspace, ScopeID: "w2", Name: "OTHER_WS"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplySnapshot(snap); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if !m.HasSnapshot() {
		t.Fatal("expected HasSnapshot = true")
	}
	if got := len(m.GlobalSecrets()); got != 1 || m.GlobalSecrets()[0].Name != "ANTHROPIC_KEY" {
		t.Fatalf("unexpected GlobalSecrets: %+v", m.GlobalSecrets())
	}
	if got := len(m.WorkspaceSecrets("w1")); got != 1 || m.WorkspaceSecrets("w1")[0].Name != "DB_PASS" {
		t.Fatalf("unexpected WorkspaceSecrets(w1): %+v", m.WorkspaceSecrets("w1"))
	}
	if got := len(m.AllForScope("w1")); got != 2 {
		t.Fatalf("expected 2 secrets for w1 scope, got %d", got)
	}

	putEv, err := json.Marshal(client.SecretTopicEvent{
		SecretID: "s4", ScopeKind: client.SecretScopeGlobal, Name: "OPENAI_KEY",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyEvent(client.EventSecretPut, putEv); err != nil {
		t.Fatalf("ApplyEvent put: %v", err)
	}
	if _, ok := m.Lookup("s4"); !ok {
		t.Fatal("expected s4 after secret_put event")
	}

	delEv, err := json.Marshal(client.SecretTopicEvent{SecretID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyEvent(client.EventSecretDeleted, delEv); err != nil {
		t.Fatalf("ApplyEvent deleted: %v", err)
	}
	if _, ok := m.Lookup("s1"); ok {
		t.Fatal("expected s1 to be removed after secret_deleted event")
	}
}

func TestWorkerPutZeroesPlaintextAndMintsCommandIDs(t *testing.T) {
	t.Parallel()
	fc := &fakeSecretsClient{
		listRes: []client.SecretMeta{
			{SecretID: "s1", ScopeKind: client.SecretScopeGlobal, Name: "TOKEN"},
		},
	}
	w := NewWorker(func() Client { return fc }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	val := []byte("super-secret-value-123")
	req := Request{
		Kind: OpPutSecret,
		Put: client.PutSecretInput{
			ScopeKind:   client.SecretScopeGlobal,
			Name:        "TOKEN",
			Description: "API token",
			Value:       val,
		},
	}
	w.Do(&req)

	// Caller's slice must be zeroed immediately by Do.
	for i, b := range val {
		if b != 0 {
			t.Fatalf("caller secret byte[%d] not zeroed: %d", i, b)
		}
	}

	res := <-w.Results()
	if res.Err != nil {
		t.Fatalf("OpPutSecret failed: %v", res.Err)
	}
	if res.Secret.SecretID != "sec-TOKEN" {
		t.Fatalf("unexpected SecretID: %q", res.Secret.SecretID)
	}
	if len(fc.puts) != 1 || fc.puts[0].CommandID == "" {
		t.Fatalf("expected minted CommandID, got %+v", fc.puts)
	}
	if string(fc.puts[0].Value) != "super-secret-value-123" {
		t.Fatalf("fake client received wrong value: %q", fc.puts[0].Value)
	}
	for i, b := range fc.captured[0] {
		if b != 0 {
			t.Fatalf("worker secret byte[%d] not zeroed after PutSecret: %d", i, b)
		}
	}

	delReq := Request{Kind: OpDeleteSecret, SecretID: "sec-TOKEN"}
	w.Do(&delReq)
	delRes := <-w.Results()
	if delRes.Err != nil || !delRes.Deleted {
		t.Fatalf("OpDeleteSecret failed: %+v", delRes)
	}
	if len(fc.deletes) != 1 || fc.deletes[0].cmdID == "" || fc.deletes[0].secretID != "sec-TOKEN" {
		t.Fatalf("unexpected delete call: %+v", fc.deletes)
	}
}

func TestWorkerZeroesPlaintextWhenDisconnected(t *testing.T) {
	t.Parallel()
	w := NewWorker(func() Client { return nil }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	val := []byte("ephemeral-token")
	req := Request{
		Kind: OpPutSecret,
		Put:  client.PutSecretInput{ScopeKind: client.SecretScopeGlobal, Name: "K", Value: val},
	}
	w.Do(&req)
	res := <-w.Results()
	if !errors.Is(res.Err, client.ErrDisconnected) {
		t.Fatalf("expected ErrDisconnected, got %v", res.Err)
	}
	for i, b := range val {
		if b != 0 {
			t.Fatalf("byte[%d] not zeroed on disconnect: %d", i, b)
		}
	}
}
