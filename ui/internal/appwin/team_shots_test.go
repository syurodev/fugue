package appwin

import (
	"image"
	"testing"
	"time"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	shotUserMessage      = "user_message"
	shotAssistantMessage = "assistant_message"
	shotTeamSession      = "team-chat"
	shotTeamAgent        = "sess-team-agent"
	shotTeamBoardInChat  = "team-board-in-chat"
)

func teamShots() []shotSpec {
	return []shotSpec{
		{name: "team-channel-light", scene: tabChannel, light: true},
		{name: "team-tree-dark", scene: tabTree},
		{name: "team-members-agent-light", scene: tabMembers, light: true},
		{name: "team-chat-light", scene: shotTeamSession, light: true},
		{name: "team-chat-dark", scene: shotTeamSession},
		{name: "team-in-chat-dark", scene: shotTeamBoardInChat},
	}
}

func teamShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	sc := buildSlotGridShotScreens(t, "")
	view := teamTreeFixture()
	view.Summary.SessionID = shotSlotSessID
	view.Brief = "Prepare the preview build for release."
	if scene != tabTree {
		view.Summary.State = channelActive
	}
	view.Tasks[2].SessionID = shotTeamAgent
	view.Tasks[2].WriteScope = []string{"internal/parser/**"}
	view.Tasks[2].DependsOn = []string{testTeamEditor}
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: shotSlotSessID, Vendor: client.VendorClaude, State: stateReady},
		{ID: shotTeamAgent, Vendor: client.VendorCodex, State: stateReady},
	}})
	sc.transcripts[shotTeamAgent] = session.NewTranscript(shotTeamAgent)
	sc.transcripts[shotTeamAgent].ApplyNewestPage(teamShotTranscript(true))
	lead := session.NewTranscript(shotSlotSessID)
	lead.ApplyNewestPage(teamShotTranscript(false))
	sc.transcripts[shotSlotSessID] = lead
	id := view.Summary.TeamID
	sc.teamViews[id] = view
	sc.teamSummaries = []client.TeamSummary{view.Summary}
	sc.teamLines[id] = teamShotLines()
	if scene == shotTeamSession || scene == shotTeamBoardInChat {
		sc.teamShell(shotSlotSessID).showTeam = scene == shotTeamBoardInChat
		sc.composer.stateFor(shotSlotSessID).area.SetText("Can you summarize what is ready for review?")
		return sc
	}
	sc.grid.openMain(SlotRef{Kind: KindTeam, ID: id})
	board := sc.boardFor(teamBoardKey{team: id, main: true})
	board.tab = scene
	board.channel.lastRead[""] = 41
	board.channel.reopen()
	board.channel.needsAnchor = false
	if scene == tabMembers {
		sc.openTeamAgent(id, view.Tasks[2].TaskID)
	}
	return sc
}

func teamShotLines() []client.ChannelLine {
	now := time.Unix(1_700_000_000, 0)
	return []client.ChannelLine{
		{ChannelSeq: 41, Kind: "plan", SenderKind: "agent", SenderTaskID: proposalRoleLead, CreatedAt: now, Content: "Prepare the preview build and verify the parser before integration."},
		{ChannelSeq: 42, Kind: client.InteractionKindQuestion, SenderKind: "agent", SenderTaskID: testTeamReviewer, RecipientTaskID: testTeamEditor, ThreadRoot: testTeamReviewer, CreatedAt: now, Content: "Should the release notes cover the behavior in #41?"},
		{ChannelSeq: 43, Kind: "send", SenderKind: channelUser, RecipientTaskID: testTeamReviewer, ThreadRoot: testTeamReviewer, CreatedAt: now, Content: "Yes, include a short example."},
		{ChannelSeq: 44, Kind: "stall", SenderKind: "watchdog", ThreadRoot: testTeamIntegration, CreatedAt: now, Content: "Integration is waiting for the build checks."},
		{ChannelSeq: 45, Kind: channelApproval, SenderKind: "system", ThreadRoot: testTeamReviewer, CreatedAt: now, Content: "approval request req-review (review result)"},
	}
}

func teamShotTranscript(agent bool) []client.TranscriptItem {
	question := "Get the parser fixes ready for the preview release. Please call out anything that still needs a decision."
	reply := "The parser update is ready. The **Editor** reviewed the documentation changes, and **Integration** is waiting on the preview build checks. The **Reviewer** has one open question about the migration example."
	if agent {
		question = "Check that the release notes explain how to update older parser configurations."
		reply = "The migration example matches the updated parser behavior. I’ve asked the Editor whether to include it in the release notes."
	}
	return []client.TranscriptItem{
		{ItemID: "release-question", Kind: shotUserMessage, Sequence: 1, Revision: 1, View: client.TranscriptView{Text: question}},
		{ItemID: "release-reply", Kind: shotAssistantMessage, Sequence: 2, Revision: 1, View: client.TranscriptView{Text: reply}},
	}
}

func renderTeamShot(t *testing.T, spec shotSpec) *image.RGBA {
	t.Helper()
	size := image.Pt(1480, 860)
	if spec.scene == shotTeamBoardInChat {
		size.Y = 980
	}
	if spec.scene == shotTeamSession {
		size.Y = 720
	}
	win, err := glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Dark(sh)
	if spec.light {
		th = theme.Light(sh)
	}
	env := &kit.Env{Th: &th, Sh: sh, UIScale: 1, ReducedMotion: true}
	sc := teamShotScreens(t, spec.scene)
	img := renderShotFrames(t, win, env, sc, size, spec.name)
	if sc.lastErr != "" || isUniformRGBA(img) {
		t.Fatalf("invalid shot %s: %s", spec.name, sc.lastErr)
	}
	return img
}

func TestTeamBoardShots(t *testing.T) {
	t.Parallel()
	for _, spec := range teamShots() {
		shottest.Compare(t, spec.name, renderTeamShot(t, spec))
	}
}
