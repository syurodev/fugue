package appwin

import (
	"context"
	"encoding/json"
	"image"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"
	interactionv1 "github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1/interactionv1connect"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	proposalShotTitle      = "Parser maintenance"
	proposalShotWidth      = 1480
	proposalShotHeight     = 1040
	proposalShotName       = "team-proposal-edited-light"
	proposalFixtureTeam    = "team-proposal"
	proposalFixtureRequest = "req-proposal"
	proposalFixtureModel   = "gpt-5.5"
)

func proposalFixture(t *testing.T, ic inbox.Client) (*screens, *client.PendingInteraction) {
	t.Helper()
	sc := buildInboxConfirmScreens(t, ic)
	req := &client.PendingInteraction{
		RequestID: proposalFixtureRequest, Incarnation: testInboxInc, SessionID: testSecretSessionID,
		Tool: proposalTool, Kind: client.InteractionKindQuestion, Summary: proposalShotTitle,
		Body: "Estimated usage: 8% of the quota window. The reviewer account is near its quota gate.",
		Questions: []client.InteractionQuestion{{
			QuestionID: proposalDecision, AllowText: true, Options: []client.InteractionOption{
				{ID: proposalApprove}, {ID: proposalEdited}, {ID: proposalSolo}, {ID: proposalReject},
			},
		}},
	}
	raw, err := json.Marshal([]client.PendingInteraction{*req})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyInteractionDelivery(hostconn.Update{
		Topic:    topicInteraction,
		Snapshot: &client.Snapshot{Incarnation: testInboxInc, Sequence: 2, State: raw},
	})
	view := client.TeamView{
		Summary: client.TeamSummary{TeamID: proposalFixtureTeam, SessionID: testSecretSessionID, State: "proposed"},
		Title:   proposalShotTitle, Brief: "Update the lexer and review the parser changes.", PendingRequestID: req.RequestID,
		Budget: &client.TeamBudget{MaxAgents: 4, MaxDurationSeconds: 1800},
		Tasks: []client.TaskNode{
			{TaskID: proposalFixtureTeam + ".lead", HierarchyRole: proposalRoleLead, CatalogRole: "coordinator", Goal: "Coordinate the changes.", Vendor: client.VendorCodex, Model: "gpt-5.4"},
			{TaskID: proposalFixtureTeam + ".lexer", ParentTaskID: proposalFixtureTeam + ".lead", HierarchyRole: testTeamMember, CatalogRole: "coder", Goal: "Update lexer tokenization.", WriteScope: []string{"internal/lexer/**"}, Vendor: client.VendorCodex, Model: "gpt-5.4"},
			{TaskID: proposalFixtureTeam + ".review", ParentTaskID: proposalFixtureTeam + ".lead", HierarchyRole: testTeamMember, CatalogRole: testTeamReviewer, Goal: "Review the parser changes.", Vendor: client.VendorClaude, Model: testTeamSonnet},
		},
	}
	sc.teamViews[proposalFixtureTeam] = view
	return sc, req
}

func proposalFixtureEnv(t *testing.T) *kit.Env {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(sh)
	return &kit.Env{Th: &th, Sh: sh, UIScale: 1, ReducedMotion: true}
}

func setFixtureProposalEdits(sc *screens, req *client.PendingInteraction) *cardFormState {
	form := sc.inboxView.formFor(req)
	view := sc.teamViews[proposalFixtureTeam]
	form.proposal = newProposalCard(&view)
	form.proposal.models["coder"].SetText(proposalFixtureModel)
	form.proposal.toggleRemoval("review")
	return form
}

type proposalAnswerHost struct {
	interactionv1connect.UnimplementedInteractionServiceHandler
	answers chan *interactionv1.AnswerRequest
}

func (h *proposalAnswerHost) Answer(_ context.Context, req *connect.Request[interactionv1.AnswerRequest]) (*connect.Response[interactionv1.AnswerResponse], error) {
	h.answers <- req.Msg
	return connect.NewResponse(&interactionv1.AnswerResponse{RequestId: req.Msg.GetRequestId(), Outcome: req.Msg.GetOutcome()}), nil
}

func TestProposalCardSubmitsExactEdits(t *testing.T) {
	t.Parallel()
	host := &proposalAnswerHost{answers: make(chan *interactionv1.AnswerRequest, 1)}
	conn, _ := serveFakeVendorHost(t, func(mux *http.ServeMux) {
		mux.Handle(interactionv1connect.NewInteractionServiceHandler(host))
	})
	sc, req := proposalFixture(t, conn)
	form := setFixtureProposalEdits(sc, req)
	env := proposalFixtureEnv(t)
	router := new(input.Router)
	size := image.Pt(shotWindowW, shotWindowH)
	runAppFrame(env, sc, router, size)
	runAppFrame(env, sc, router, size)
	focus := layout.Context{Source: router.Source()}
	focus.Execute(key.FocusCmd{Tag: form.allowOnceBtn.Tag()})
	runAppFrame(env, sc, router, size)
	router.Queue(key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release})
	runAppFrame(env, sc, router, size)
	res := awaitAppInboxResult(t, sc)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	select {
	case got := <-host.answers:
		assertExactProposalAnswer(t, req, got)
	default:
		t.Fatal("host did not receive the proposal")
	}
}

func assertExactProposalAnswer(t *testing.T, req *client.PendingInteraction, got *interactionv1.AnswerRequest) {
	t.Helper()
	if got.GetRequestId() != req.RequestID || got.GetIncarnation() != testInboxInc ||
		got.GetOutcome() != client.InteractionOutcomeAnswered || got.GetCommandId() == "" || len(got.GetAnswers()) != 1 {
		t.Fatal("proposal decision did not preserve its pin, outcome and command ID")
	}
	answer := got.GetAnswers()[0]
	if answer.GetQuestionId() != proposalDecision || !cmp.Equal(answer.GetOptionIds(), []string{proposalEdited}) {
		t.Fatal("proposal decision did not select Approve with edits")
	}
	const want = `{"version":1,"roles":[{"name":"coder","model":"gpt-5.5"}],"remove_tasks":["review"]}`
	if answer.GetText() != want {
		t.Fatal("proposal decision did not carry the exact edited document")
	}
}

func renderProposalShot(t *testing.T, win *headless.Window) *image.RGBA {
	t.Helper()
	sc, req := proposalFixture(t, nil)
	sc.osAppearance = newShotAppearanceSource(true)
	sc.grid.drawer = ""
	sc.grid.drawerPresence.Snap(false)
	sc.openSession(req.SessionID)
	tr := session.NewTranscript(req.SessionID)
	tr.ApplyNewestPage(proposalShotTranscript())
	sc.transcripts[req.SessionID] = tr
	setFixtureProposalEdits(sc, req)
	env := proposalFixtureEnv(t)
	img := renderShotFrames(t, win, env, sc, image.Pt(proposalShotWidth, proposalShotHeight), proposalShotName)
	if sc.lastErr != "" {
		t.Fatal(sc.lastErr)
	}
	return img
}

func proposalShotTranscript() []client.TranscriptItem {
	return []client.TranscriptItem{
		{ItemID: "proposal-user", Kind: shotUserMessage, Sequence: 1, Revision: 1, View: client.TranscriptView{Text: "Could a small team update the lexer and review the parser changes?"}},
		{
			ItemID: "proposal-item", Kind: "team_proposal", Sequence: 2, Revision: 1,
			Metadata: client.TranscriptMetadata{ProposalID: proposalFixtureTeam, AgentCount: 2},
			View:     client.TranscriptView{Title: proposalShotTitle, Text: "Update the lexer and review the parser changes."},
		},
	}
}

func TestProposalCardShot(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(proposalShotWidth, proposalShotHeight)
	if err != nil {
		t.Fatal(err)
	}
	img := renderProposalShot(t, win)
	if isUniformRGBA(img) {
		t.Fatal("proposal shot is uniform")
	}
	shottest.Compare(t, proposalShotName, img)
}
