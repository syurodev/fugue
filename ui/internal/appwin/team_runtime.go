package appwin

import (
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/team"
)

type teamRuntime struct {
	teamWorker      *team.Worker
	teamSidebarList layout.List
	teamSidebarRows map[string]*kit.ListRowState
	teamViews       map[string]client.TeamView
	teamCatalog     map[string][]team.Role
	teamRefresh     map[string]bool
	teamLines       map[string][]client.ChannelLine
	teamSummaries   []client.TeamSummary
	teamBoards      map[teamBoardKey]*teamBoardState
	teamShells      map[string]*teamShell
	teamAgents      map[string]*teamAgentDetail
	teamPending     map[team.Request]bool
	teamFailedReads map[team.Request]bool
	teamTopics      map[string]bool
	teamRead        map[string]map[string]int64
	teamEpoch       uint64
}

func newTeamRuntime() teamRuntime {
	return teamRuntime{
		teamSidebarList: layout.List{Axis: layout.Vertical},
		teamSidebarRows: make(map[string]*kit.ListRowState),
		teamViews:       make(map[string]client.TeamView),
		teamCatalog:     make(map[string][]team.Role),
		teamRefresh:     make(map[string]bool),
		teamLines:       make(map[string][]client.ChannelLine),
		teamBoards:      make(map[teamBoardKey]*teamBoardState),
		teamShells:      make(map[string]*teamShell),
		teamAgents:      make(map[string]*teamAgentDetail),
		teamPending:     make(map[team.Request]bool),
		teamFailedReads: make(map[team.Request]bool),
		teamTopics:      make(map[string]bool),
		teamRead:        make(map[string]map[string]int64),
	}
}

func (s *screens) requestTeam(id string) {
	if id == "" || s.teamWorker == nil {
		return
	}
	if !s.teamTopics[id] {
		s.m.AddTopic(client.TopicTeamChannel + id)
		s.teamTopics[id] = true
	}
	if _, loaded := s.teamViews[id]; !loaded {
		req := team.Request{Op: team.Read, Team: id, Epoch: s.teamEpoch}
		if !s.teamFailedReads[req] {
			s.queueTeam(req)
		}
	}
}

func (s *screens) queueTeam(req team.Request) bool {
	if s.teamWorker == nil {
		return false
	}
	req.Epoch = s.teamEpoch
	if s.teamPending[req] {
		return false
	}
	if !s.teamWorker.Submit(&req) {
		return false
	}
	if req.Op != team.Post && req.Op != team.Recover {
		s.teamPending[req] = true
	}
	return true
}

func (s *screens) listTeams() {
	s.queueTeam(team.Request{Op: team.List})
}

func (s *screens) postTeamMessage(id, thread, target, body string) bool {
	return s.vault.Unlocked() && s.queueTeam(team.Request{Op: team.Post, Team: id, Thread: thread, Task: target, Text: body})
}

func (s *screens) applyTeamTopic(u hostconn.Update) {
	id := strings.TrimPrefix(u.Topic, client.TopicTeamChannel)
	if s.vault.Unlocked() {
		s.applyTeamChannel(id, &u)
	}
	s.refreshTeam(id)
}

func (s *screens) refreshTeam(id string) {
	if s.teamPending[team.Request{Op: team.Read, Team: id, Epoch: s.teamEpoch}] {
		s.teamRefresh[id] = true
		return
	}
	s.queueTeam(team.Request{Op: team.Read, Team: id})
}

func (s *screens) applyTeamChannel(id string, u *hostconn.Update) {
	if u.Snapshot != nil {
		snap, err := client.DecodeTeamChannelSnapshot(u.Snapshot.State)
		s.fail(err)
		if err == nil {
			s.teamLines[id] = team.MergeChannel(s.teamLines[id], snap.Lines)
		}
	} else if u.Event != nil {
		event, err := client.DecodeTeamChannelEvent(u.Event.Type, u.Event.Payload)
		s.fail(err)
		if err == nil && !event.Locked {
			s.teamLines[id] = team.MergeChannel(s.teamLines[id], []client.ChannelLine{event.Line})
		}
	}
}

func (s *screens) drainTeamResults() {
	if s.teamWorker == nil {
		return
	}
	results := s.teamWorker.Drain()
	for i := range results {
		s.applyTeamResult(&results[i])
	}
}

func (s *screens) applyTeamResult(r *team.Result) {
	req := team.Request{Op: r.Op, Team: r.Team, Task: r.Task, Before: r.Before, Epoch: r.Epoch}
	delete(s.teamPending, req)
	if r.Epoch != s.teamEpoch {
		return
	}
	if r.Err != nil {
		if r.Op == team.Read || r.Op == team.Diff {
			s.teamFailedReads[req] = true
		}
		s.fail(r.Err)
		return
	}
	delete(s.teamFailedReads, req)
	switch r.Op {
	case team.List:
		s.teamSummaries = r.Summaries
	case team.Read:
		s.applyTeamView(r)
	case team.Catalog:
		s.teamCatalog[r.Team] = r.Roles
	case team.Diff:
		if s.vault.Unlocked() {
			s.teamDetail(r.Team, r.Task).view = &r.View
		}
	case team.Page:
		s.applyTeamPage(r)
	case team.Post, team.Recover:
		s.queueTeam(team.Request{Op: team.Read, Team: r.Team})
	}
}

func (s *screens) updateTeamSummary(summary client.TeamSummary) {
	for i := range s.teamSummaries {
		if s.teamSummaries[i].TeamID == summary.TeamID {
			s.teamSummaries[i] = summary
			return
		}
	}
	s.teamSummaries = append(s.teamSummaries, summary)
}

func (s *screens) clearTeamPayloads() {
	s.teamEpoch++
	clear(s.teamPending)
	clear(s.teamFailedReads)
	clear(s.teamViews)
	clear(s.teamLines)
	clear(s.teamBoards)
	clear(s.teamAgents)
	clear(s.teamRefresh)
	clear(s.teamCatalog)
	for _, form := range s.inboxView.forms {
		form.proposal = nil
	}
}

func (s *screens) resetTeamPayloads() {
	s.clearTeamPayloads()
	s.listTeams()
	for id := range s.teamTopics {
		s.queueTeam(team.Request{Op: team.Read, Team: id})
		if s.vault.Unlocked() {
			s.queueTeam(team.Request{Op: team.Page, Team: id})
		}
	}
}

func (s *screens) applyTeamPage(r *team.Result) {
	if s.vault.Locked() {
		return
	}
	s.teamLines[r.Team] = team.MergeChannel(s.teamLines[r.Team], r.Lines)
	if len(r.Lines) < 100 {
		for key, board := range s.teamBoards {
			if key.team == r.Team {
				board.channel.historyEnd = true
			}
		}
	}
}

func (s *screens) failedTeamRead(op team.Operation, id, task string) bool {
	return s.teamFailedReads[team.Request{Op: op, Team: id, Task: task, Epoch: s.teamEpoch}]
}

func (s *screens) applyTeamView(result *team.Result) {
	id := result.Team
	if s.vault.Unlocked() || result.View.VaultLocked {
		s.teamViews[id] = result.View
	}
	s.updateTeamSummary(result.View.Summary)
	if s.teamRefresh[id] {
		delete(s.teamRefresh, id)
		s.refreshTeam(id)
	}
	workspaceID := result.View.Summary.WorkspaceID
	if _, loaded := s.teamCatalog[workspaceID]; !loaded && workspaceID != "" && result.View.Summary.State == "proposed" && s.vault.Unlocked() {
		s.queueTeam(team.Request{Op: team.Catalog, Team: workspaceID})
	}
}
