package appwin

import (
	"encoding/json"
	"image"
	"testing"
	"time"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/vault"
)

const (
	shotSecretsStore        = "secrets-store"
	shotSecretsAddDialog    = "secrets-add-dialog"
	shotInboxSecretQuestion = "inbox-secret-question"
)

func secretsShotSpecs() []shotSpec {
	return []shotSpec{
		{name: shotSecretsStore + "-light-ui100", scene: shotSecretsStore, light: true, uiScale: 1.0},
		{name: shotSecretsStore + "-dark-ui100", scene: shotSecretsStore, light: false, uiScale: 1.0},
		{name: shotSecretsAddDialog + "-light-ui100", scene: shotSecretsAddDialog, light: true, uiScale: 1.0},
		{name: shotInboxSecretQuestion + "-light-ui100", scene: shotInboxSecretQuestion, light: true, uiScale: 1.0},
		{name: shotInboxSecretQuestion + "-dark-ui100", scene: shotInboxSecretQuestion, light: false, uiScale: 1.0},
	}
}

func buildSecretsShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	fc := &fakeAppSecretsClient{}
	ic := &fakeAppInboxClient{inc: testInboxInc}
	sc := buildTestSecretsScreens(t, fc, ic)

	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testInboxInc}))

	snap, err := json.Marshal(client.SecretsSnapshot{
		Secrets: []client.SecretMeta{
			{
				SecretID:    "sec-1",
				ScopeKind:   client.SecretScopeGlobal,
				Name:        "ANTHROPIC_API_KEY",
				Description: "Shared Claude vendor credential",
				UpdatedAt:   time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC),
			},
			{
				SecretID:    "sec-2",
				ScopeKind:   client.SecretScopeGlobal,
				Name:        "GITHUB_TOKEN",
				Description: "Read-only token for PR checks",
				UpdatedAt:   time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC),
			},
			{
				SecretID:    "sec-3",
				ScopeKind:   client.SecretScopeWorkspace,
				ScopeID:     testSettingsWSID,
				Name:        "STAGING_DB_URL",
				Description: "Alpha workspace database connection",
				UpdatedAt:   time.Date(2026, 11, 22, 18, 15, 0, 0, time.UTC),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applySecretsDelivery(hostconn.Update{Topic: topicSecrets, Snapshot: &client.Snapshot{State: snap}})

	if scene == shotInboxSecretQuestion {
		setupInboxSecretShot(t, sc)
		return sc
	}

	openVisibleSlot(sc, SlotRef{Kind: KindSecrets})
	if scene == shotSecretsAddDialog {
		sc.secretsView.addDlg.open(testSettingsWSID)
		sc.secretsView.addDlg.name.SetText("OPENAI_API_KEY")
		sc.secretsView.addDlg.description.SetText("Used by vendor sessions")
		sc.secretsView.addDlg.value.SetText("sample")
	}
	return sc
}

func setupInboxSecretShot(t *testing.T, sc *screens) {
	t.Helper()
	req := client.PendingInteraction{
		RequestID:       "req-sec-shot-1",
		Incarnation:     testInboxInc,
		SessionID:       testSecretSessionID,
		Kind:            client.InteractionKindQuestion,
		Summary:         "Vendor session needs a credential to access the staging API.",
		OfferedOutcomes: []string{client.InteractionOutcomeAnswered, client.InteractionOutcomeCancelled},
		Questions: []client.InteractionQuestion{
			{
				QuestionID: "q-api-key",
				Prompt:     "Enter staging API token",
				AllowText:  true,
				Secret:     true,
			},
		},
	}
	isnap, err := json.Marshal([]client.PendingInteraction{req})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyInteractionDelivery(hostconn.Update{
		Topic:    topicInteraction,
		Snapshot: &client.Snapshot{Incarnation: testInboxInc, Sequence: 1, State: isnap},
	})
	form := sc.inboxView.formFor(&req)
	qf := form.questions[0]
	qf.secretInput.SetText("sk-staging-demo")
	qf.saveSecret = true
	qf.saveName.SetText("STAGING_API_TOKEN")
	qf.saveScopeIdx = 1
	openVisibleSlot(sc, SlotRef{Kind: KindInbox})
}

func renderSecretsShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	return renderAppShot(t, win, spec, buildSecretsShotScreens(t, spec.scene))
}

func TestSecretsAndInboxSecretQuestionShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range secretsShotSpecs() {
		img := renderSecretsShot(t, win, spec)
		if isUniformRGBA(img) {
			t.Fatalf("secrets shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	if !imagesDifferRGBA(rendered[shotSecretsStore+"-light-ui100"], rendered[shotSecretsStore+"-dark-ui100"]) {
		t.Fatal("secrets store light and dark shots are identical")
	}
	if !imagesDifferRGBA(rendered[shotInboxSecretQuestion+"-light-ui100"], rendered[shotInboxSecretQuestion+"-dark-ui100"]) {
		t.Fatal("inbox secret question light and dark shots are identical")
	}
}
