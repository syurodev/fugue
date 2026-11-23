package appwin

import (
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/team"
)

type teamRuntime struct {
	teamWorker      *team.Worker
	teamSidebarList layout.List
	teamSidebarRows map[string]*teamSidebarRow
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
	teamShown       map[string]bool
	teamLastShown   map[string]time.Time
	teamTopicPort   teamTopicPort
	teamPosts       map[uint64]*teamPostWait
	teamPostSeq     uint64
	teamRead        map[string]map[string]int64
	teamEpoch       uint64
}

func newTeamRuntime() teamRuntime {
	return teamRuntime{
		teamSidebarList: layout.List{Axis: layout.Vertical},
		teamSidebarRows: make(map[string]*teamSidebarRow),
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
		teamShown:       make(map[string]bool),
		teamLastShown:   make(map[string]time.Time),
		teamPosts:       make(map[uint64]*teamPostWait),
		teamRead:        make(map[string]map[string]int64),
	}
}

// teamTopicPort is the subscription surface of the host connection.
type teamTopicPort interface {
	AddTopic(topic string)
	RemoveTopic(topic string)
}

// teamTopicGrace is how long a team may stay off screen before its stream
// stops: maximizing a slot or switching Chat and Team skips the board for a
// few frames, and dropping the stream then would re-add it and refetch.
const teamTopicGrace = 10 * time.Second

// setTeamTopic is the single place a team channel stream starts or stops.
// It goes through the window's reference-counted hook so windows sharing a
// team keep one host stream between them.
func (s *screens) setTeamTopic(id string, active bool) {
	topic := client.TopicTeamChannel + id
	switch {
	case s.setTopic != nil:
		s.setTopic(topic, active)
	case s.teamTopicPort != nil && active:
		s.teamTopicPort.AddTopic(topic)
	case s.teamTopicPort != nil:
		s.teamTopicPort.RemoveTopic(topic)
	case active:
		s.m.AddTopic(topic)
	default:
		s.m.RemoveTopic(topic)
	}
}

// releaseTeamTopics stops the streams of teams that no frame has displayed
// for teamTopicGrace. A team shown again in time keeps its stream; one shown
// after the release re-subscribes and refreshes its view, and the kept cursor
// makes the channel catch up. A wake-up is scheduled for the earliest
// release so it still happens when nothing else invalidates the window.
func (s *screens) releaseTeamTopics(gtx *layout.Context) {
	now := gtx.Now
	var wake time.Time
	for id := range s.teamTopics {
		last, seen := s.teamLastShown[id]
		if s.teamShown[id] || !seen {
			s.teamLastShown[id] = now
			continue
		}
		due := last.Add(teamTopicGrace)
		if now.Before(due) {
			if wake.IsZero() || due.Before(wake) {
				wake = due
			}
			continue
		}
		s.setTeamTopic(id, false)
		delete(s.teamTopics, id)
		delete(s.teamRefresh, id)
		delete(s.teamLastShown, id)
	}
	clear(s.teamShown)
	if !wake.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: wake})
	}
}

func (s *screens) requestTeam(id string) {
	if id == "" || s.teamWorker == nil {
		return
	}
	s.teamShown[id] = true
	if !s.teamTopics[id] {
		s.setTeamTopic(id, true)
		s.teamTopics[id] = true
		if _, loaded := s.teamViews[id]; loaded {
			// The view went stale while no stream fed it.
			s.refreshTeam(id)
		}
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

// teamPostWait remembers which composer sent a post that has no result yet.
type teamPostWait struct {
	c            *teamChannelComposer
	thread, text string
}

// postTeamMessage enqueues one channel post and returns the token that its
// result carries; zero means nothing was queued.
func (s *screens) postTeamMessage(c *teamChannelComposer, id, thread, target, body string) uint64 {
	if !s.vault.Unlocked() {
		return 0
	}
	s.teamPostSeq++
	ref := s.teamPostSeq
	if !s.queueTeam(team.Request{Op: team.Post, Team: id, Thread: thread, Task: target, Text: body, Ref: ref}) {
		return 0
	}
	s.teamPosts[ref] = &teamPostWait{c: c, thread: thread, text: body}
	return ref
}

// finishTeamPost frees the composer and clears the draft only when the host
// accepted the message; a failed post keeps the text for another try.
func (s *screens) finishTeamPost(r *team.Result) {
	wait := s.teamPosts[r.Ref]
	if wait == nil {
		return
	}
	delete(s.teamPosts, r.Ref)
	c := wait.c
	c.posting = 0
	if r.Err != nil {
		return
	}
	// The host accepted the message, so the draft goes even when the epoch
	// moved on; a stale composer is harmless to clear.
	if c.thread == wait.thread {
		if strings.TrimSpace(c.area.Text()) == wait.text {
			c.area.SetText("")
		}
	} else if strings.TrimSpace(c.drafts[wait.thread]) == wait.text {
		delete(c.drafts, wait.thread)
	}
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
	if r.Op == team.Post {
		s.finishTeamPost(r)
	}
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
	s.applyTeamSuccess(r)
}

func (s *screens) applyTeamSuccess(r *team.Result) {
	switch r.Op {
	case team.List:
		s.teamSummaries = r.Summaries
		s.pruneTeamRead(r.Summaries)
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

// retryTeamRead forgets a failed read and queues it again.
func (s *screens) retryTeamRead(op team.Operation, id, task string) {
	delete(s.teamFailedReads, team.Request{Op: op, Team: id, Task: task, Epoch: s.teamEpoch})
	s.queueTeam(team.Request{Op: op, Team: id, Task: task})
}

// pruneTeamRead drops read markers of teams the host no longer lists.
func (s *screens) pruneTeamRead(summaries []client.TeamSummary) {
	known := make(map[string]bool, len(summaries))
	for i := range summaries {
		known[summaries[i].TeamID] = true
	}
	for id := range s.teamRead {
		if !known[id] {
			delete(s.teamRead, id)
		}
	}
	stale := false
	for id := range s.grid.doc.TeamRead {
		stale = stale || !known[id]
	}
	if !stale {
		return
	}
	// Layout saves encode asynchronously, so each document owns its own maps.
	kept := make(map[string]map[string]int64, len(s.grid.doc.TeamRead))
	for id, read := range s.grid.doc.TeamRead {
		if known[id] {
			kept[id] = read
		}
	}
	s.grid.doc.TeamRead = kept
	s.grid.dirty = true
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
