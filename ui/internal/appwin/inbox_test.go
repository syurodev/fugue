package appwin

import (
	"encoding/json"
	"image"
	"testing"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const testInboxSess1 = "sess-inbox-1"

func newInboxTestEnv(t *testing.T) *kit.Env {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	return &kit.Env{Th: &th, Sh: sh, ReducedMotion: true}
}

func awaitAppInboxResult(t *testing.T, sc *screens) inbox.Result {
	t.Helper()
	select {
	case res := <-sc.inboxWorker.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("timed out waiting for inbox worker: %v", t.Context().Err())
		return inbox.Result{}
	}
}

func TestInboxSidebarBadgesReconnectAndExternalResolution(t *testing.T) {
	t.Parallel()
	ic := &fakeAppInboxClient{
		inc: testInboxInc,
		list: client.PendingInteractionList{
			Sequence: 3,
			Requests: []client.PendingInteraction{
				{
					RequestID: "r1", Incarnation: testInboxInc, SessionID: testInboxSess1,
					Tool: "shell", Kind: client.InteractionKindConfirm, Summary: "first",
					OfferedOutcomes: []string{client.InteractionOutcomeAllowedOnce, client.InteractionOutcomeDenied},
				},
				{
					RequestID: "r2", Incarnation: testInboxInc, SessionID: "sess-inbox-2",
					Tool: "shell", Kind: client.InteractionKindConfirm, Summary: "second",
					OfferedOutcomes: []string{client.InteractionOutcomeAllowedOnce, client.InteractionOutcomeDenied},
				},
			},
		},
	}
	sc := buildInboxConfirmScreens(t, ic)
	sc.reconnected()
	sc.applyInboxResults([]inbox.Result{awaitAppInboxResult(t, sc)})

	if sc.inbox.PendingCount() != 2 || sc.inbox.PendingCountForSession(testInboxSess1) != 1 {
		t.Fatalf("counts after reconnect = total:%d s1:%d", sc.inbox.PendingCount(), sc.inbox.PendingCountForSession(testInboxSess1))
	}

	// External resolution drops r1 and updates badges.
	resolvedBytes, err := json.Marshal(client.InteractionResolvedEvent{RequestID: "r1", Outcome: client.InteractionOutcomeExpired})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyInteractionDelivery(hostconn.Update{
		Topic: topicInteraction,
		Event: &client.Event{Sequence: 4, Type: client.EventInteractionResolved, Payload: resolvedBytes},
	})
	if sc.inbox.PendingCount() != 1 || sc.inbox.PendingCountForSession(testInboxSess1) != 0 {
		t.Fatalf("counts after external resolve = total:%d s1:%d", sc.inbox.PendingCount(), sc.inbox.PendingCountForSession(testInboxSess1))
	}

	// Ctrl/Cmd+Shift+I toggles inbox view without plain letter shortcuts.
	env := newInboxTestEnv(t)
	router := new(input.Router)
	sc.inboxOpen = false
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	router.Queue(key.Event{Name: key.Name("I"), Modifiers: key.ModShortcut | key.ModShift, State: key.Press})
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	if !sc.inboxOpen {
		t.Fatal("Ctrl/Cmd+Shift+I did not open the inbox")
	}
}

func TestInboxSelectAndQuestionCardsWithIMEComposing(t *testing.T) {
	t.Parallel()
	ic := &fakeAppInboxClient{inc: testInboxInc}
	sc := buildInboxConfirmScreens(t, ic)
	env := newInboxTestEnv(t)
	router := new(input.Router)

	selectReq, questionReq := seedSelectAndQuestion(t, sc)
	_ = selectReq
	assertSelectDefaultsToDeny(t, env, sc, router)
	assertQuestionIMEAndSubmit(t, env, sc, router, &questionReq)

	ic.mu.Lock()
	defer ic.mu.Unlock()
	if len(ic.answers) != 2 {
		t.Fatalf("recorded answers = %d, want 2 (%+v)", len(ic.answers), ic.answers)
	}
	if ic.answers[0].Outcome != client.InteractionOutcomeDenied || ic.answers[1].Outcome != client.InteractionOutcomeAnswered {
		t.Fatalf("outcomes = %+v", ic.answers)
	}
	if len(ic.answers[1].Answers) != 1 || ic.answers[1].Answers[0].Text != "tiếng Việt" {
		t.Fatalf("question answer = %+v", ic.answers[1].Answers)
	}
}

func seedSelectAndQuestion(t *testing.T, sc *screens) (selectReq, questionReq client.PendingInteraction) {
	t.Helper()
	selectReq = client.PendingInteraction{
		RequestID: "req-select", Incarnation: testInboxInc, SessionID: testInboxSess1,
		Tool: "picker", Kind: client.InteractionKindSelect, Summary: "Choose target",
		OfferedOutcomes: []string{client.InteractionOutcomeAllowedOnce, client.InteractionOutcomeDenied},
		Options:         []client.InteractionOption{{ID: "opt-1", Label: "Option One", Description: "Safe desc"}},
	}
	questionReq = client.PendingInteraction{
		RequestID: "req-q", Incarnation: testInboxInc, SessionID: testInboxSess1,
		Kind: client.InteractionKindQuestion, Summary: "Which mode?",
		OfferedOutcomes: []string{client.InteractionOutcomeAnswered, client.InteractionOutcomeCancelled},
		Questions: []client.InteractionQuestion{{
			QuestionID: "q1", Prompt: "Enter custom note [not-a-link](https://example.com)", AllowText: true,
		}},
	}
	snap, err := json.Marshal([]client.PendingInteraction{selectReq, questionReq})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyInteractionDelivery(hostconn.Update{
		Topic:    topicInteraction,
		Snapshot: &client.Snapshot{Incarnation: testInboxInc, Sequence: 5, State: snap},
	})
	return selectReq, questionReq
}

func assertSelectDefaultsToDeny(t *testing.T, env *kit.Env, sc *screens, router *input.Router) {
	t.Helper()
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	router.Queue(
		key.Event{Name: key.NameReturn, State: key.Press},
		key.Event{Name: key.NameReturn, State: key.Release},
	)
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	sc.applyInboxResults([]inbox.Result{awaitAppInboxResult(t, sc)})
}

func assertQuestionIMEAndSubmit(t *testing.T, env *kit.Env, sc *screens, router *input.Router, questionReq *client.PendingInteraction) {
	t.Helper()
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	pinQ := inbox.Pin{RequestID: "req-q", Incarnation: testInboxInc}
	formQ := sc.inboxView.formFor(questionReq)
	focusTagInFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH), formQ.questions[0].textInput.Tag())
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	router.Queue(
		key.CompositionEvent{Start: 0, End: 2},
		key.EditEvent{Text: "vi", Range: key.Range{Start: 0, End: 0}},
		key.Event{Name: key.NameReturn, State: key.Press},
	)
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	if !sc.inbox.CanAnswer(pinQ) || formQ.busy {
		t.Fatal("Enter while IME composing submitted the question card")
	}

	formQ.questions[0].textInput.SetText("tiếng Việt")
	submitInboxAnswer(sc, formQ, pinQ, &client.AnswerInteractionInput{
		Outcome: client.InteractionOutcomeAnswered,
		Answers: buildQuestionAnswers(questionReq, formQ),
	})
	sc.applyInboxResults([]inbox.Result{awaitAppInboxResult(t, sc)})
}

func focusTagInFrame(env *kit.Env, sc *screens, router *input.Router, size image.Point, tag event.Tag) {
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
	gtx.Execute(key.FocusCmd{Tag: tag})
	env.FrameEnd(&gtx)
	router.Frame(&ops)
}
