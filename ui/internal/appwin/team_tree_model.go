package appwin

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// teamTreeState keeps separate viewport positions for the two agent views.
type teamTreeState struct {
	nodes       map[string]*teamAgentCard
	seenSeq     int64
	seenLocked  bool
	initialized bool
	roots       []*teamAgentCard
	ordered     []*teamAgentCard
	visible     []teamTreeRow
	dirty       bool
	treeList    layout.List
	membersList layout.List
	treeFocus   string
	review      *kit.ButtonState
	needs       *teamCountChip
	failed      *teamCountChip
}

type teamAgentCard struct {
	node        client.TaskNode
	title       string
	model       string
	elapsed     string
	tag         kit.TagProps
	tree        teamCardPaint
	member      teamCardPaint
	collapse    *kit.IconButtonState
	collapsed   bool
	children    []*teamAgentCard
	parent      *teamAgentCard
	latest      client.ChannelLine
	receivedSeq int64
}

type teamTreeRow struct {
	card      *teamAgentCard
	depth     int
	continued uint64
	sibling   bool
}

// syncAgents uses the team sequence to know when to compare snapshots. A
// sibling changing the sequence does not invalidate an unchanged card.
func (s *teamTreeState) syncAgents(view *client.TeamView) {
	if s.initialized && s.seenSeq == view.Summary.LastSeq && s.seenLocked == view.VaultLocked {
		return
	}
	s.initialized, s.seenSeq, s.seenLocked = true, view.Summary.LastSeq, view.VaultLocked
	next := make(map[string]*teamAgentCard, len(view.Tasks))
	ordered := make([]*teamAgentCard, 0, len(view.Tasks))
	for i := range view.Tasks {
		node := &view.Tasks[i]
		card := s.nodes[node.TaskID]
		if card == nil {
			card = new(teamAgentCard)
		}
		card.apply(node, view.VaultLocked)
		next[node.TaskID] = card
		ordered = append(ordered, card)
	}
	s.nodes, s.ordered = next, ordered
	s.linkAgents()
	s.dirty = true
}

type teamAgentRevision struct {
	id, state, goal, vendor, model, role, hierarchy string
	attempt                                         uint32
	lastChannelSeq, elapsedMs                       int64
}

func revisionOfAgent(n *client.TaskNode) teamAgentRevision {
	return teamAgentRevision{
		id: n.TaskID, state: n.State, goal: n.Goal, vendor: n.Vendor, model: n.Model,
		role: n.CatalogRole, hierarchy: n.HierarchyRole, attempt: n.Attempt,
		lastChannelSeq: n.LastChannelSeq, elapsedMs: n.ElapsedMs,
	}
}

func (c *teamAgentCard) apply(n *client.TaskNode, locked bool) {
	if locked {
		c.latest.Content = ""
		c.member.valid = false
		c.member.ops.Reset()
		c.tree.ops.Reset()
		c.tree.valid = false
	}
	// Protected fields may disappear on lock without a new event sequence.
	if revisionOfAgent(&c.node) != revisionOfAgent(n) {
		c.tree.valid, c.member.valid = false, false
		c.title = teamRoleTitle(n)
		c.model = teamAgentModel(n)
		c.elapsed = teamElapsed(n.ElapsedMs)
		c.tag = teamAgentState(n.State)
	}
	c.node = *n
}

func (s *teamTreeState) linkAgents() {
	s.roots = s.roots[:0]
	for _, c := range s.ordered {
		c.children, c.parent = c.children[:0], nil
	}
	for _, c := range s.ordered {
		parent := s.nodes[c.node.ParentTaskID]
		if parent == nil || parent == c {
			s.roots = append(s.roots, c)
			continue
		}
		c.parent = parent
		parent.children = append(parent.children, c)
	}
	orderTeamRetries(s.roots)
	for _, c := range s.ordered {
		orderTeamRetries(c.children)
	}
}

func orderTeamRetries(siblings []*teamAgentCard) {
	for i := 0; i < len(siblings); i++ {
		current := siblings[i]
		if current.node.RetryOf == "" {
			continue
		}
		parent := slices.IndexFunc(siblings, func(before *teamAgentCard) bool {
			return before.node.TaskID == current.node.RetryOf
		})
		if parent < 0 || parent == i || parent == i-1 {
			continue
		}
		// Rotate only the affected span so unrelated siblings keep their order.
		if parent < i {
			copy(siblings[parent+2:i+1], siblings[parent+1:i])
			siblings[parent+1] = current
		} else {
			copy(siblings[i:parent], siblings[i+1:parent+1])
			siblings[parent] = current
		}
	}
}

func (s *teamTreeState) treeRows() []teamTreeRow {
	if s.dirty {
		s.visible = appendTeamTreeRows(s.visible[:0], s.roots, 0, 0)
		s.dirty = false
	}
	return s.visible
}

func appendTeamTreeRows(rows []teamTreeRow, cards []*teamAgentCard, depth int, continued uint64) []teamTreeRow {
	for i, c := range cards {
		sibling := i < len(cards)-1
		rows = append(rows, teamTreeRow{card: c, depth: depth, continued: continued, sibling: sibling})
		if c.collapsed {
			continue
		}
		ancestors := continued
		if depth > 0 && sibling {
			ancestors |= 1 << (depth - 1)
		}
		rows = appendTeamTreeRows(rows, c.children, depth+1, ancestors)
	}
	return rows
}

func (s *teamTreeState) jumpToAgent(id string) {
	card := s.nodes[id]
	if card == nil {
		return
	}
	for p := card.parent; p != nil; p = p.parent {
		p.collapsed = false
	}
	s.dirty = true
	s.treeFocus = id
	for i, row := range s.treeRows() {
		if row.card == card {
			s.treeList.ScrollTo(i)
			return
		}
	}
}

func teamRoleTitle(n *client.TaskNode) string {
	hierarchy := strings.ReplaceAll(n.HierarchyRole, "_", " ")
	if hierarchy == proposalRoleLead {
		return "Lead"
	}
	if n.CatalogRole == "" {
		if hierarchy == "" {
			return "Agent"
		}
		return hierarchy
	}
	if n.HierarchyRole == "team_manager" {
		return n.CatalogRole + " · sub-leader"
	}
	return n.CatalogRole
}

func teamAgentModel(n *client.TaskNode) string {
	if n.Model == "" || n.Vendor == "" {
		return n.Vendor + n.Model
	}
	return n.Vendor + " / " + n.Model
}

func teamElapsed(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	d := time.Duration(ms) * time.Millisecond
	return max(d.Round(time.Second), time.Second).String()
}

func teamAgentState(state string) kit.TagProps {
	p := kit.TagProps{Text: strings.ReplaceAll(state, "_", " "), Variant: kit.TagStatus}
	if p.Text == "" {
		p.Text = "Unknown"
	}
	switch state {
	case "starting", stateRunning, "checking", "integrating":
		p.Status = theme.StatusInfo
	case teamStateDone:
		p.Status = theme.StatusSuccess
	case teamStateAsking, "needs_you", teamStateReview:
		p.Text, p.Status = teamNeedsLabel, theme.StatusWarning
	case channelFailed:
		p.Status = theme.StatusDanger
	case teamStateUnknown:
		p.Variant, p.Hue = kit.TagHue, theme.HueViolet
	default:
		p.Variant = kit.TagNeutral
	}
	return p
}

func teamSequence(seq int64) string {
	if seq <= 0 {
		return "—"
	}
	return "#" + strconv.FormatInt(seq, 10)
}
