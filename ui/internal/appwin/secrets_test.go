package appwin

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

type fakeAppSecretsClient struct {
	mu      sync.Mutex
	listRes []client.SecretMeta
	puts    []client.PutSecretInput
	deletes []string
}

func (f *fakeAppSecretsClient) ListSecrets(context.Context, string, string) ([]client.SecretMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listRes, nil
}

func (f *fakeAppSecretsClient) PutSecret(_ context.Context, in *client.PutSecretInput) (client.SecretMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *in
	cp.Value = bytes.Clone(in.Value)
	f.puts = append(f.puts, cp)
	clear(in.Value)
	return client.SecretMeta{
		SecretID:    "sec-" + in.Name,
		ScopeKind:   in.ScopeKind,
		ScopeID:     in.ScopeID,
		Name:        in.Name,
		Description: in.Description,
		UpdatedAt:   time.Unix(1700000000, 0).UTC(),
	}, nil
}

func (f *fakeAppSecretsClient) DeleteSecret(_ context.Context, _, secretID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, secretID)
	return true, nil
}

func buildTestSecretsScreens(t *testing.T, scClient secrets.Client, ic inbox.Client) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	if scClient != nil {
		w := secrets.NewWorker(func() secrets.Client { return scClient }, nil)
		w.Start(t.Context())
		t.Cleanup(w.Stop)
		sc.secretsWorker = w
	}
	if ic != nil {
		iw := inbox.NewWorker(func() inbox.Client { return ic }, nil)
		iw.Start(t.Context())
		t.Cleanup(iw.Stop)
		sc.inboxWorker = iw
	}
	wsSnap, err := json.Marshal([]workspace.Summary{
		{ID: "ws-1", Name: "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyWorkspaceDelivery(hostconn.Update{
		Topic:    topicWorkspaces,
		Snapshot: &client.Snapshot{State: wsSnap},
	})
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "sess-1", Vendor: client.VendorCodex, State: "running", WorkspaceID: "ws-1"},
	}})
	sc.open = "sess-1"
	return sc
}

func awaitAppSecretsResult(t *testing.T, sc *screens) secrets.Result {
	t.Helper()
	select {
	case res := <-sc.secretsWorker.Results():
		return res
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for secrets worker result")
		return secrets.Result{}
	}
}

func TestSecretsScreenLockedVaultMetadataVisibleAndMutationsZeroBuffer(t *testing.T) {
	t.Parallel()
	fc := &fakeAppSecretsClient{}
	sc := buildTestSecretsScreens(t, fc, nil)

	// Vault is locked initially (INV-09): topic snapshot still populates metadata.
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "inc-sec"}))
	snap, err := json.Marshal(client.SecretsSnapshot{
		Secrets: []client.SecretMeta{
			{SecretID: "s-glob", ScopeKind: client.SecretScopeGlobal, Name: "ANTHROPIC_KEY", Description: "Global API key"},
			{SecretID: "s-ws", ScopeKind: client.SecretScopeWorkspace, ScopeID: "ws-1", Name: "DB_TOKEN", Description: "Workspace DB"},
			{SecretID: "s-other", ScopeKind: client.SecretScopeWorkspace, ScopeID: "ws-2", Name: "OTHER_WS"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applySecretsDelivery(hostconn.Update{Topic: topicSecrets, Snapshot: &client.Snapshot{State: snap}})

	if !sc.vault.Locked() {
		t.Fatal("expected vault to be locked")
	}
	if got := len(sc.secretsView.filteredItems(sc.secrets, "ws-1")); got != 2 {
		t.Fatalf("expected 2 secrets (Global + ws-1) visible while locked, got %d", got)
	}

	// Unlock vault and test Add -> Replace -> Delete, verifying plaintext clearing.
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateUnlocked, Revision: 2, Incarnation: "inc-sec"}))

	sc.secretsView.addDlg.open("ws-1")
	sc.secretsView.addDlg.name.SetText("NEW_TOKEN")
	sc.secretsView.addDlg.description.SetText("Added via UI")
	sc.secretsView.addDlg.value.SetText("secret-value-add")
	sc.secretsView.addDlg.submitSecret(sc)

	if sc.secretsView.addDlg.value.Text() != "" {
		t.Fatal("add dialog value field was not cleared immediately after submit")
	}
	sc.applySecretsResults([]secrets.Result{awaitAppSecretsResult(t, sc)})
	if _, ok := sc.secrets.Lookup("sec-NEW_TOKEN"); !ok {
		t.Fatal("expected sec-NEW_TOKEN in model after PutSecret")
	}

	// Replace value on s-glob.
	glob, _ := sc.secrets.Lookup("s-glob")
	sc.secretsView.replaceDlg.open(&glob)
	sc.secretsView.replaceDlg.value.SetText("replaced-secret-val")
	sc.secretsView.replaceDlg.submitReplace(sc)

	if sc.secretsView.replaceDlg.value.Text() != "" {
		t.Fatal("replace dialog value field was not cleared immediately after submit")
	}
	sc.applySecretsResults([]secrets.Result{awaitAppSecretsResult(t, sc)})

	// Delete s-glob.
	sc.secretsView.deleteDlg.open(&glob)
	req := secrets.Request{Kind: secrets.OpDeleteSecret, SecretID: glob.SecretID}
	sc.secretsWorker.Do(&req)
	sc.applySecretsResults([]secrets.Result{awaitAppSecretsResult(t, sc)})
	if _, ok := sc.secrets.Lookup("s-glob"); ok {
		t.Fatal("expected s-glob removed after DeleteSecret")
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.puts) != 2 || string(fc.puts[0].Value) != "secret-value-add" || string(fc.puts[1].Value) != "replaced-secret-val" {
		t.Fatalf("unexpected PutSecret calls: %+v", fc.puts)
	}
	if len(fc.deletes) != 1 || fc.deletes[0] != "s-glob" {
		t.Fatalf("unexpected DeleteSecret calls: %+v", fc.deletes)
	}
}

func TestInboxSecretQuestionMaskedInputAndSecretPicker(t *testing.T) {
	t.Parallel()
	ic := &fakeAppInboxClient{inc: "inc-sec-q"}
	sc := buildTestSecretsScreens(t, nil, ic)
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-sec-q"}))
	sc.secrets.ApplyList([]client.SecretMeta{
		{SecretID: "s-saved-1", ScopeKind: client.SecretScopeGlobal, Name: "GH_TOKEN"},
	})

	qDirect := client.PendingInteraction{
		RequestID:       "req-sec-1",
		Incarnation:     "inc-sec-q",
		SessionID:       "sess-1",
		Kind:            client.InteractionKindQuestion,
		Summary:         "Provide API token",
		OfferedOutcomes: []string{client.InteractionOutcomeAnswered, client.InteractionOutcomeCancelled},
		Questions: []client.InteractionQuestion{
			{QuestionID: "q-token", Prompt: "Enter API token", AllowText: true, Secret: true},
		},
	}
	qPick := client.PendingInteraction{
		RequestID:       "req-sec-2",
		Incarnation:     "inc-sec-q",
		SessionID:       "sess-1",
		Kind:            client.InteractionKindQuestion,
		Summary:         "Select saved token",
		OfferedOutcomes: []string{client.InteractionOutcomeAnswered, client.InteractionOutcomeCancelled},
		Questions: []client.InteractionQuestion{
			{QuestionID: "q-pick", Prompt: "Choose GitHub token", AllowText: true, Secret: true},
		},
	}
	snap, err := json.Marshal([]client.PendingInteraction{qDirect, qPick})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyInteractionDelivery(hostconn.Update{
		Topic:    topicInteraction,
		Snapshot: &client.Snapshot{Incarnation: "inc-sec-q", Sequence: 1, State: snap},
	})

	// Case 1: masked input + save to Secret Store.
	form1 := sc.inboxView.formFor(&qDirect)
	qf1 := form1.questions[0]
	qf1.secretMode = 0
	qf1.secretInput.SetText("raw-api-secret-xyz")
	qf1.saveSecret = true
	qf1.saveName.SetText("MY_API_TOKEN")
	qf1.saveScopeIdx = 1 // workspace
	if !canSubmitQuestions(&qDirect, form1) {
		t.Fatal("expected direct secret question form to be submittable")
	}
	answers1 := buildQuestionAnswers(&qDirect, form1, "ws-1")
	if qf1.secretInput.Text() != "" {
		t.Fatal("secretInput was not cleared immediately after buildQuestionAnswers")
	}
	submitInboxAnswer(sc, form1, inbox.Pin{RequestID: "req-sec-1", Incarnation: "inc-sec-q"}, &client.AnswerInteractionInput{
		Outcome: client.InteractionOutcomeAnswered,
		Answers: answers1,
	})
	sc.applyInboxResults([]inbox.Result{awaitAppInboxResult(t, sc)})

	// Case 2: pick existing secret_id.
	form2 := sc.inboxView.formFor(&qPick)
	qf2 := form2.questions[0]
	qf2.secretMode = 1
	qf2.selectedSecID = "s-saved-1"
	if !canSubmitQuestions(&qPick, form2) {
		t.Fatal("expected secret picker question form to be submittable")
	}
	answers2 := buildQuestionAnswers(&qPick, form2, "ws-1")
	submitInboxAnswer(sc, form2, inbox.Pin{RequestID: "req-sec-2", Incarnation: "inc-sec-q"}, &client.AnswerInteractionInput{
		Outcome: client.InteractionOutcomeAnswered,
		Answers: answers2,
	})
	sc.applyInboxResults([]inbox.Result{awaitAppInboxResult(t, sc)})

	// Zero-retention check: caller's slice must be zeroed by worker.
	for i, b := range answers1[0].SecretValue {
		if b != 0 {
			t.Fatalf("answers1 SecretValue byte[%d] not zeroed: %d", i, b)
		}
	}

	ic.mu.Lock()
	defer ic.mu.Unlock()
	if len(ic.answers) != 2 {
		t.Fatalf("expected 2 AnswerInteraction calls, got %d", len(ic.answers))
	}
	a1 := ic.answers[0].Answers[0]
	if a1.SaveSecretName != "MY_API_TOKEN" || a1.SaveScopeKind != client.SecretScopeWorkspace || a1.SaveScopeID != "ws-1" {
		t.Fatalf("unexpected save-to-store fields: %+v", a1)
	}
	a2 := ic.answers[1].Answers[0]
	if a2.SecretID != "s-saved-1" || len(a2.SecretValue) != 0 {
		t.Fatalf("unexpected secret_id answer: %+v", a2)
	}
}
