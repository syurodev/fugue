package appwin

import (
	"context"
	"encoding/json"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// inboxSlotShotW is the window width at which the shell folds the inbox into
// its drawer.
const inboxSlotShotW = 700

const (
	shotInboxConfirm = "inbox-confirm"
	shotInboxSlot    = "inbox-confirm-slot"
	testInboxInc     = "inc-inbox-shot"
)

type fakeAppInboxClient struct {
	mu      sync.Mutex
	inc     string
	list    client.PendingInteractionList
	answers []client.AnswerInteractionInput
}

func (f *fakeAppInboxClient) HostStatus(context.Context) (client.HostStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.HostStatus{Incarnation: f.inc}, nil
}

func (f *fakeAppInboxClient) ListPendingInteractions(context.Context) (client.PendingInteractionList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list, nil
}

func (f *fakeAppInboxClient) AnswerInteraction(_ context.Context, in *client.AnswerInteractionInput) (client.InteractionAnswerResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, *in)
	return client.InteractionAnswerResult{RequestID: in.RequestID, Outcome: in.Outcome}, nil
}

func inboxShotSpecs() []shotSpec {
	return []shotSpec{
		{name: shotInboxConfirm + "-light-ui100", scene: shotInboxConfirm, light: true, uiScale: 1.0},
		{name: shotInboxConfirm + "-dark-ui100", scene: shotInboxConfirm, light: false, uiScale: 1.0},
		{name: shotInboxSlot + "-light-ui100", scene: shotInboxSlot, light: true, uiScale: 1.0},
		{name: shotInboxSlot + "-dark-ui125", scene: shotInboxSlot, light: false, uiScale: 1.25},
	}
}

func longConfirmDetail() string {
	var b strings.Builder
	b.WriteString(`{"command":"bash -lc 'for i in $(seq 1 30); do echo line-$i; done'",`)
	b.WriteString(`"payload":"UNTRUSTED_TAIL_MARKER_END"}`)
	for i := range 24 {
		b.WriteString("\nline ")
		b.WriteRune(rune('A' + i%26))
		b.WriteString(": full verbatim detail must scroll without truncation")
	}
	return b.String()
}

func buildInboxConfirmScreens(t *testing.T, ic inbox.Client) *screens {
	t.Helper()
	return buildInboxScreensWithDetail(t, ic, longConfirmDetail())
}

func buildInboxScreensWithDetail(t *testing.T, ic inbox.Client, detail string) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testInboxInc})
	if ic != nil {
		w := inbox.NewWorker(func() inbox.Client { return ic }, nil)
		w.Start(t.Context())
		t.Cleanup(w.Stop)
		sc.inboxWorker = w
	}
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: testSecretSessionID, Vendor: client.VendorCodex, State: "awaiting_approval"},
	}})
	req := client.PendingInteraction{
		RequestID:       "req-confirm-1",
		Incarnation:     testInboxInc,
		SessionID:       testSecretSessionID,
		Tool:            "bash",
		Kind:            client.InteractionKindConfirm,
		Risk:            "exec",
		EnvTag:          "prod",
		Summary:         "Execute command [not-a-link](https://evil.invalid) <script>alert(1)</script>",
		Body:            "Relayed verbatim from vendor.",
		Detail:          detail,
		OfferedOutcomes: []string{client.InteractionOutcomeAllowedOnce, client.InteractionOutcomeAllowedSession, client.InteractionOutcomeDenied},
		GrantScopeLabel: "bash in prod",
	}
	snap, err := json.Marshal([]client.PendingInteraction{req})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyInteractionDelivery(hostconn.Update{
		Topic:    topicInteraction,
		Snapshot: &client.Snapshot{Incarnation: testInboxInc, Sequence: 1, State: snap},
	})
	openVisibleSlot(sc, SlotRef{Kind: KindInbox})
	return sc
}

// openVisibleSlot opens ref's companion slot and slides its drawer out, so
// the body renders and interacts at the shared shot width, where the shell
// folds every regular column into the edge tab strip (§5.3).
func openVisibleSlot(sc *screens, ref SlotRef) {
	sc.grid.openCompanion(ref)
	for _, p := range regularCells(sc.grid.doc) {
		if slot, _ := slotAt(sc.grid.doc, p); slot.Ref.Kind == ref.Kind {
			sc.grid.drawer = p.key()
			sc.grid.drawerPresence.Snap(true)
			return
		}
	}
}

func renderInboxShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Dark(sh)
	if spec.light {
		th = theme.Light(sh)
	}
	env := &kit.Env{Th: &th, Sh: sh, UIScale: spec.uiScale, ReducedMotion: true}
	sc := buildInboxConfirmScreens(t, nil)
	size := image.Pt(shotWindowW, shotWindowH)
	if spec.scene == shotInboxSlot {
		// A window this narrow folds the inbox into the 300 dp drawer, the
		// width the companion slot must wrap to (FUG-40).
		size = image.Pt(inboxSlotShotW, shotWindowH)
		var err error
		if win, err = glwin.Open(size.X, size.Y); err != nil {
			t.Fatalf("open headless window for %s: %v", spec.name, err)
		}
	}
	img := renderShotFrames(t, win, env, sc, size, spec.name)
	if sc.lastErr != "" {
		t.Fatalf("inbox shot %s recorded error: %s", spec.name, sc.lastErr)
	}
	return img
}

// TestInboxConfirmShowsFullDetailAndDefaultsToSafeChoice verifies that confirm
// cards preserve the full detail verbatim in a scrollable area, focus the safe
// choice (Deny) on open so Enter never approves, and render light/dark shots.
func TestInboxConfirmShowsFullDetailAndDefaultsToSafeChoice(t *testing.T) {
	t.Parallel()
	assertConfirmFullDetailAndSafeEnter(t)

	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range inboxShotSpecs() {
		img := renderInboxShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("inbox shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	if !imagesDifferRGBA(rendered[shotInboxConfirm+"-light-ui100"], rendered[shotInboxConfirm+"-dark-ui100"]) {
		t.Fatal("inbox confirm light and dark shots are identical")
	}
}

func assertConfirmFullDetailAndSafeEnter(t *testing.T) {
	t.Helper()
	ic := &fakeAppInboxClient{inc: testInboxInc}
	sc := buildInboxConfirmScreens(t, ic)

	items := sc.inbox.Items()
	if len(items) != 1 || !strings.Contains(items[0].Detail, "UNTRUSTED_TAIL_MARKER_END") || items[0].Detail != longConfirmDetail() {
		t.Fatalf("confirm detail was truncated or altered: %+v", items)
	}

	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	env := &kit.Env{Th: &th, Sh: sh, ReducedMotion: true}
	router := new(input.Router)
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))

	// Pressing Enter on a freshly opened card must activate Deny, never Allow.
	router.Queue(
		key.Event{Name: key.NameReturn, State: key.Press},
		key.Event{Name: key.NameReturn, State: key.Release},
	)
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))

	select {
	case res := <-sc.inboxWorker.Results():
		sc.applyInboxResults([]inbox.Result{res})
	case <-t.Context().Done():
		t.Fatalf("timed out waiting for Enter answer: %v", t.Context().Err())
	}

	ic.mu.Lock()
	defer ic.mu.Unlock()
	if len(ic.answers) != 1 || ic.answers[0].Outcome != client.InteractionOutcomeDenied {
		t.Fatalf("Enter on fresh confirm card produced %+v, want single denied answer", ic.answers)
	}
}

func runAppFrame(env *kit.Env, sc *screens, router *input.Router, size image.Point) {
	var ops op.Ops
	now := time.Unix(1_700_000_000, 0)
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(device), Constraints: layout.Exact(size),
		Source: router.Source(), Now: now,
	}
	env.Begin(now)
	env.TrackInput(&gtx)
	drawWindow(&gtx, env, sc)
	env.FrameEnd(&gtx)
	router.Frame(&ops)
}
