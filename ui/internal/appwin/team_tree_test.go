package appwin

import (
	"image"
	"strconv"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
)

const teamTreeAgentCount = 12

func teamTreeFixture() client.TeamView {
	tasks := []client.TaskNode{
		{
			TaskID: proposalRoleLead, HierarchyRole: proposalRoleLead, Vendor: client.VendorCodex, Model: "gpt-5", State: stateRunning,
			Goal: "Coordinate the release checklist", ElapsedMs: 196_000,
		},
		{
			TaskID: testTeamEditor, ParentTaskID: proposalRoleLead, HierarchyRole: "team_manager", CatalogRole: "Editor",
			Vendor: client.VendorClaude, Model: testTeamSonnet, State: teamStateDone, Goal: "Review documentation changes", ElapsedMs: 174_000,
		},
		{
			TaskID: testTeamReviewer, ParentTaskID: testTeamEditor, HierarchyRole: testTeamMember, CatalogRole: "Reviewer",
			Vendor: client.VendorCodex, Model: "gpt-5", State: teamStateAsking, Goal: "Confirm terminology for the migration", ElapsedMs: 92_000,
		},
		{
			TaskID: testTeamIntegration, ParentTaskID: proposalRoleLead, HierarchyRole: testTeamMember, CatalogRole: "Integration",
			Vendor: client.VendorCodex, Model: testTeamMini, State: "stopped", Goal: "Verify the preview build", ElapsedMs: 57_000,
		},
		{
			TaskID: testTeamRetry, ParentTaskID: proposalRoleLead, RetryOf: testTeamIntegration, HierarchyRole: testTeamMember, CatalogRole: "Integration",
			Vendor: client.VendorCodex, Model: testTeamMini, State: channelFailed, Goal: "Verify the preview build", Attempt: 2, ElapsedMs: 34_000,
		},
	}
	return client.TeamView{Summary: client.TeamSummary{
		TeamID: "release-team", LastSeq: 10, TaskCount: 5, RunningCount: 1, AskingCount: 1, FailedCount: 1, State: teamFinalReview,
	}, Title: "Release checklist", Tasks: tasks}
}

type teamTreeHarness struct {
	env   *kit.Env
	sc    screens
	board teamBoardState
	ops   op.Ops
	view  client.TeamView
	step  int
}

func newTeamTreeHarness(t *testing.T) *teamTreeHarness {
	t.Helper()
	h := &teamTreeHarness{env: newInboxTestEnv(t), view: teamTreeFixture()}
	for i := len(h.view.Tasks); i < teamTreeAgentCount; i++ {
		h.view.Tasks = append(h.view.Tasks, client.TaskNode{
			TaskID: "agent-" + strconv.Itoa(i), ParentTaskID: proposalRoleLead, HierarchyRole: testTeamMember, CatalogRole: "Tester",
			Vendor: client.VendorCodex, Model: testTeamMini, State: stateRunning, Goal: "Cover release scenario " + strconv.Itoa(i),
		})
	}
	h.view.Summary.TaskCount = teamTreeAgentCount
	h.render()
	return h
}

func (h *teamTreeHarness) render() {
	h.ops.Reset()
	now := time.Unix(1_700_000_000, 0).Add(time.Duration(h.step) * time.Millisecond)
	gtx := layout.Context{
		Ops: &h.ops, Now: now, Constraints: layout.Exact(image.Pt(810, 1600)),
		Metric: h.env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
	}
	h.env.Begin(now)
	h.sc.layoutTeamTree(&gtx, h.env, &h.view, &h.board)
	h.env.FrameEnd(&gtx)
}

func (h *teamTreeHarness) update() string {
	task := &h.view.Tasks[h.step%len(h.view.Tasks)]
	task.LastChannelSeq++
	task.ElapsedMs += 1000
	if task.State == stateRunning {
		task.State = "checking"
	} else {
		task.State = stateRunning
	}
	h.view.Summary.LastSeq++
	h.step++
	return task.TaskID
}

func TestTeamTreeReusesUnchangedAgentLayouts(t *testing.T) {
	t.Parallel()
	h := newTeamTreeHarness(t)
	for range 2 * teamTreeAgentCount {
		before := captureTeamLayoutCalls(&h.board.teamTreeState)
		changed := h.update()
		h.render()
		for id, card := range h.board.nodes {
			if got := before[id] != card.tree.revision; got != (id == changed) {
				t.Fatalf("agent %q rebuilt = %v after changing %q", id, got, changed)
			}
		}
	}
}

func captureTeamLayoutCalls(s *teamTreeState) map[string]uint64 {
	calls := make(map[string]uint64, len(s.nodes))
	for id, card := range s.nodes {
		calls[id] = card.tree.revision
	}
	return calls
}

func TestTeamTreeJumpReopensAncestorAndOrdersReruns(t *testing.T) {
	t.Parallel()
	var tree teamTreeState
	view := teamTreeFixture()
	view.Tasks[3], view.Tasks[4] = view.Tasks[4], view.Tasks[3]
	tree.syncAgents(&view)
	tree.nodes[testTeamEditor].collapsed = true
	rows := tree.treeRows()
	assertTeamTreeOrder(t, rows, []string{proposalRoleLead, testTeamEditor, testTeamIntegration, testTeamRetry})
	tree.jumpToAgent(testTeamReviewer)
	rows = tree.treeRows()
	assertTeamTreeOrder(t, rows, []string{proposalRoleLead, testTeamEditor, testTeamReviewer, testTeamIntegration, testTeamRetry})
	if tree.treeFocus != testTeamReviewer || rows[2].depth != 2 || tree.treeList.Position.First != 2 {
		t.Fatalf("jump did not focus nested agent at row 2: %q, %+v", tree.treeFocus, tree.treeList.Position)
	}
}

func assertTeamTreeOrder(t *testing.T, rows []teamTreeRow, want []string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("tree has %d rows, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if got := row.card.node.TaskID; got != want[i] {
			t.Fatalf("row %d has task %q, want %q", i, got, want[i])
		}
	}
}

const (
	testTeamSonnet      = "sonnet"
	testTeamMini        = "gpt-5-mini"
	testTeamReviewer    = "reviewer"
	testTeamEditor      = "editor"
	testTeamIntegration = "integration"
	testTeamRetry       = "integration~r2"
	testTeamMember      = "member"
)
