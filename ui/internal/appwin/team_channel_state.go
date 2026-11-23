package appwin

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	channelGeneral  = "General"
	channelApproval = "approval"
	channelFailed   = "failed"
	channelStall    = "stall"
	channelActive   = "active"
)

type teamChannelPane struct {
	teamID                             string
	thread, filter                     string
	threadList                         layout.List
	list                               layout.List
	threadRows                         map[string]*kit.ListRowState
	messageRows                        map[int64]*teamMessageRow
	filterButton, older, threadsButton *kit.ButtonState
	filterMenu, threadsMenu            *kit.MenuState
	composer                           *teamChannelComposer
	lastRead                           map[string]int64
	openedAt                           int64
	needsAnchor                        bool
	historyEnd                         bool
	jump                               int64
	anchorSeq                          int64
	lastSequence                       int64
}

type teamMessageRow struct {
	number, replies, inbox *kit.ButtonState
	tag                    *kit.TagState
	markdown               mdview.State
	content                string
	document               *markdown.Doc
	refs                   map[int64]*kit.ButtonState
}

func newTeamChannelPane(lastRead map[string]int64) *teamChannelPane {
	return &teamChannelPane{
		threadList:   layout.List{Axis: layout.Vertical},
		list:         layout.List{Axis: layout.Vertical, ScrollToEnd: true},
		threadRows:   make(map[string]*kit.ListRowState),
		messageRows:  make(map[int64]*teamMessageRow),
		filterButton: kit.NewButtonState(), older: kit.NewButtonState(),
		threadsButton: kit.NewButtonState(),
		filterMenu:    kit.NewMenuState(), threadsMenu: kit.NewMenuState(),
		composer:    newTeamChannelComposer(),
		lastRead:    lastRead,
		openedAt:    lastRead[""],
		needsAnchor: true,
	}
}

func (p *teamChannelPane) reopen() {
	p.openedAt, p.needsAnchor = p.lastRead[p.thread], true
	p.jump = 0
}

func (p *teamChannelPane) switchThread(thread string) {
	if thread == p.thread {
		return
	}
	p.thread, p.anchorSeq, p.lastSequence = thread, 0, 0
	p.list.Position = layout.Position{}
	p.composer.setThread(thread)
	p.reopen()
}

func (p *teamChannelPane) messageState(seq int64) *teamMessageRow {
	state := p.messageRows[seq]
	if state == nil {
		state = &teamMessageRow{
			number: kit.NewButtonState(), replies: kit.NewButtonState(),
			inbox: kit.NewButtonState(), tag: kit.NewTagState(),
		}
		p.messageRows[seq] = state
	}
	return state
}

type teamChannelThread struct {
	node       *client.TaskNode
	id, parent string
	depth      int
}

func channelThreadRoot(taskID string) string {
	root, attempt, split := strings.Cut(taskID, "~r")
	if !split || root == "" {
		return taskID
	}
	if _, err := strconv.ParseUint(attempt, 10, 32); err != nil {
		return taskID
	}
	return root
}

func modelChannelThreads(view *client.TeamView) []teamChannelThread {
	byRoot := make(map[string]*client.TaskNode, len(view.Tasks))
	order := make([]string, 0, len(view.Tasks))
	for i := range view.Tasks {
		node := &view.Tasks[i]
		root := channelThreadRoot(node.TaskID)
		if prev := byRoot[root]; prev == nil {
			order = append(order, root)
			byRoot[root] = node
		} else if node.Attempt > prev.Attempt {
			byRoot[root] = node
		}
	}
	byParent := make(map[string][]teamChannelThread)
	for _, id := range order {
		node := byRoot[id]
		parent := channelThreadRoot(node.ParentTaskID)
		if byRoot[parent] == nil || byRoot[parent].HierarchyRole == proposalRoleLead {
			parent = ""
		}
		byParent[parent] = append(byParent[parent], teamChannelThread{node: node, id: id, parent: parent})
	}
	out := make([]teamChannelThread, 0, len(order))
	for _, item := range byParent[""] {
		out = appendChannelThread(out, byParent, item)
	}
	return out
}

func appendChannelThread(out []teamChannelThread, byParent map[string][]teamChannelThread, row teamChannelThread) []teamChannelThread {
	out = append(out, row)
	for _, child := range byParent[row.id] {
		child.depth = row.depth + 1
		out = appendChannelThread(out, byParent, child)
	}
	return out
}

func filterChannelThread(filter, state string) bool {
	switch filter {
	case teamStateAsking, channelFailed:
		return state == filter
	case stateRunning:
		_, tone := channelThreadStatus(state)
		return tone == kit.ToneInfo
	default:
		return true
	}
}

func channelThreadStatus(state string) (string, kit.ListRowTone) {
	switch state {
	case teamStateAsking:
		return teamNeedsLabel, kit.ToneWarning
	case stateRunning, "starting", "checking", "integrating":
		return "Running", kit.ToneInfo
	case channelFailed:
		return failedLabel, kit.ToneDanger
	case teamStateDone:
		return "Done", kit.ToneSuccess
	default:
		return state, kit.ToneNeutral
	}
}

type teamChannelData struct {
	lines           []*client.ChannelLine
	bySequence      map[int64]*client.ChannelLine
	byTask          map[string]*client.TaskNode
	unread, replies map[string]int
}

func modelTeamChannel(lines []client.ChannelLine, view *client.TeamView, p *teamChannelPane) teamChannelData {
	data := teamChannelData{
		lines:      make([]*client.ChannelLine, 0, len(lines)),
		bySequence: make(map[int64]*client.ChannelLine, len(lines)),
		byTask:     make(map[string]*client.TaskNode, len(view.Tasks)),
		unread:     make(map[string]int), replies: make(map[string]int),
	}
	for i := range view.Tasks {
		data.byTask[view.Tasks[i].TaskID] = &view.Tasks[i]
	}
	for i := range lines {
		data.includeLine(&lines[i], p)
	}
	slices.SortFunc(data.lines, func(a, b *client.ChannelLine) int {
		return cmp.Compare(a.ChannelSeq, b.ChannelSeq)
	})
	return data
}

func (d *teamChannelData) includeLine(line *client.ChannelLine, p *teamChannelPane) {
	d.bySequence[line.ChannelSeq] = line
	if line.ThreadRoot != "" {
		d.replies[line.ThreadRoot]++
	}
	if line.ChannelSeq > p.lastRead[line.ThreadRoot] && line.SenderKind != channelUser {
		d.unread[line.ThreadRoot]++
	}
	if p.thread == "" || p.thread == line.ThreadRoot {
		d.lines = append(d.lines, line)
	}
}

func (d *teamChannelData) role(taskID string) string {
	if task := d.byTask[taskID]; task != nil {
		return channelRole(task)
	}
	return taskID
}

func channelRole(task *client.TaskNode) string {
	return cmp.Or(task.CatalogRole, task.HierarchyRole, task.TaskID)
}

func channelSender(line *client.ChannelLine, d *teamChannelData) string {
	switch line.SenderKind {
	case channelUser:
		return "You"
	case "agent":
		return cmp.Or(d.role(line.SenderTaskID), "Lead")
	case "watchdog":
		return "Watchdog"
	default:
		return "Fugue"
	}
}

func channelKindTag(kind string) kit.TagProps {
	p := kit.TagProps{Text: kind, Variant: kit.TagStatus}
	switch kind {
	case "", "message", "send":
		p.Text = ""
	case channelStall, channelApproval, client.InteractionKindQuestion:
		p.Status = theme.StatusWarning
	case channelFailed:
		p.Status = theme.StatusDanger
	case "result":
		p.Status = theme.StatusSuccess
	default:
		p.Status = theme.StatusInfo
	}
	return p
}

func firstChannelUnread(lines []*client.ChannelLine, through int64) int {
	return slices.IndexFunc(lines, func(line *client.ChannelLine) bool {
		return line.ChannelSeq > through && line.SenderKind != channelUser
	})
}

func (p *teamChannelPane) scrollChannel(lines []*client.ChannelLine) int {
	if len(lines) == 0 {
		return -1
	}
	unread := firstChannelUnread(lines, p.openedAt)
	switch {
	case p.jump > 0:
		i := channelLineIndex(lines, p.jump)
		if i >= 0 {
			p.list.ScrollTo(i)
			p.jump, p.needsAnchor = 0, false
		}
	case p.needsAnchor:
		p.needsAnchor = false
		if unread >= 0 && p.openedAt != 0 {
			p.list.ScrollTo(unread)
		}
	case p.list.Position.BeforeEnd && p.anchorSeq != 0 && lines[0].ChannelSeq != p.lastSequence:
		if i := channelLineIndex(lines, p.anchorSeq); i >= 0 {
			p.list.Position.First = i
		}
	}
	p.lastSequence = lines[0].ChannelSeq
	return unread
}

func channelLineIndex(lines []*client.ChannelLine, seq int64) int {
	i, found := slices.BinarySearchFunc(lines, seq, func(line *client.ChannelLine, seq int64) int {
		return cmp.Compare(line.ChannelSeq, seq)
	})
	if !found {
		return -1
	}
	return i
}

func (p *teamChannelPane) readChannel(lines []*client.ChannelLine) bool {
	start, count := p.list.Position.First, p.list.Position.Count
	if start < 0 || count == 0 || start >= len(lines) {
		return false
	}
	p.anchorSeq = lines[start].ChannelSeq
	last := min(start+count-1, len(lines)-1)
	seq := lines[last].ChannelSeq
	if seq <= p.lastRead[p.thread] {
		return false
	}
	p.lastRead[p.thread] = seq
	return true
}

func (r *teamMessageRow) reference(n int64) *kit.ButtonState {
	if r.refs == nil {
		r.refs = make(map[int64]*kit.ButtonState)
	}
	if r.refs[n] == nil {
		r.refs[n] = kit.NewButtonState()
	}
	return r.refs[n]
}
