package appwin

import (
	"image"
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestTeamChannelThreadsRoutingAndInbox(t *testing.T) {
	t.Parallel()
	sc, req := proposalFixture(t, nil)
	view := teamTreeFixture()
	pane := newTeamChannelPane(map[string]int64{testTeamReviewer: 42})
	pane.switchThread(testTeamReviewer)
	data := modelTeamChannel(teamShotLines(), &view, pane)
	if got := firstChannelUnread(data.lines, pane.openedAt); got != 2 {
		t.Fatalf("first unread row = %d, want 2", got)
	}
	line := data.lines[0]
	if got := teamSequence(line.ChannelSeq); got != "#42" || channelSender(line, &data) != "Reviewer" ||
		data.role(line.RecipientTaskID) != "Editor" || channelKindTag(line.Kind).Text != client.InteractionKindQuestion {
		t.Fatal("channel row lost its number, sender, recipient or kind")
	}
	assertChannelRoutes(t, modelChannelThreads(&view))
	pane.jump = 42
	pane.scrollChannel(data.lines)
	if pane.jump != 0 || pane.list.Position.First != 0 {
		t.Fatal("message jump did not use the numbered row")
	}
	sc.openTeamInbox(req.RequestID)
	if sc.inboxView.active != (inbox.Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}) {
		t.Fatal("approval link did not select the pinned inbox request")
	}
}

func assertChannelRoutes(t *testing.T, threads []teamChannelThread) {
	t.Helper()
	if got, problem := channelMessageTarget("@reviewer Please check this", threads); got != testTeamReviewer || problem != "" {
		t.Fatalf("explicit recipient = %q, %s", got, problem)
	}
	if got, problem := channelMessageTarget("Please check this", threads); got != "" || problem != "" {
		t.Fatal("an ordinary reply must leave routing to the host's thread logic")
	}
	if got, problem := channelMessageTarget("@integration Please check this", threads); got != testTeamRetry || problem != "" {
		t.Fatal("an explicit thread-root mention must resolve to the latest attempt")
	}
}

func TestTeamChannelRestoresUnreadPositionFromLayout(t *testing.T) {
	t.Parallel()
	sc := teamShotScreens(t, tabChannel)
	id := sc.teamSummaries[0].TeamID
	board := sc.boardFor(teamBoardKey{team: id, main: true})
	board.channel.lastRead[testTeamReviewer] = 42
	sc.saveTeamPosition(board.channel)
	raw, err := EncodeLayout(sc.grid.doc)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	reopened := teamShotScreens(t, tabChannel)
	reopened.teamRuntime = newTeamRuntime()
	reopened.grid.doc = decoded.Doc
	pane := reopened.boardFor(teamBoardKey{team: id, main: true}).channel
	pane.switchThread(testTeamReviewer)
	view := teamTreeFixture()
	data := modelTeamChannel(teamShotLines(), &view, pane)
	unread := pane.scrollChannel(data.lines)
	if unread != 2 || pane.list.Position.First != unread || pane.openedAt != 42 {
		t.Fatalf("restored position = %+v, unread = %d, read through = %d", pane.list.Position, unread, pane.openedAt)
	}
}

func TestTeamSwitchPreservesChatAndComposer(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	sc := fx.sc
	view := teamTreeFixture()
	view.Summary.SessionID = sc.open
	view.Summary.State = channelActive
	id := view.Summary.TeamID
	sc.teamViews[id] = view
	sc.teamSummaries = []client.TeamSummary{view.Summary}
	sh := sc.teamShell(sc.open)
	h := newFrameRunner(t)
	size := image.Pt(800, 500)
	mode := func(g *layout.Context) { sc.layoutTeamMode(g, h.env, sh, id) }
	h.frame(t, size, mode)
	h.router.Source().Execute(key.FocusCmd{Tag: sh.mode.Tag()})
	h.frame(t, size, mode)
	h.router.Queue(key.Event{Name: key.NameRightArrow, State: key.Press})
	h.frame(t, size, mode)
	if !sh.showTeam {
		t.Fatal("Team segment did not activate")
	}
	assertTeamComposer(t, fx, h, size)
	sc.jumpFromTeamStrip(sh, id, theme.StatusDanger)
	board := sc.boardFor(teamBoardKey{team: id, main: true})
	if !sh.showTeam || board.tab != tabTree || board.jump != testTeamRetry {
		t.Fatal("failed chip failed to select the latest failed agent")
	}
	h.frame(t, size, mode)
	h.router.Source().Execute(key.FocusCmd{Tag: sh.mode.Tag()})
	h.frame(t, size, mode)
	h.router.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	h.frame(t, size, mode)
	if sh.showTeam || board.tab != tabTree {
		t.Fatal("Chat segment changed the board's tab")
	}
}

func assertTeamComposer(t *testing.T, fx *composerFixture, h *frameRunner, size image.Point) {
	t.Helper()
	sc := fx.sc
	chat := sc.viewFor(sc.open)
	chat.ScrollTo(120)
	fx.state().area.SetText("Message the lead from Team")
	// Give the composer the fixture transport's connection state.
	frame := func(g *layout.Context) {
		sc.layoutSessionPane(g, h.env, sc.open, true, true)
	}
	h.frame(t, size, frame)
	h.router.Source().Execute(key.FocusCmd{Tag: fx.state().area.Tag()})
	h.frame(t, size, frame)
	h.tapReturn(0)
	h.frame(t, size, frame)
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].text != "Message the lead from Team" || chat.Offset() != 120 {
		t.Fatal("Team mode did not preserve the chat offset and send to the lead")
	}
}
