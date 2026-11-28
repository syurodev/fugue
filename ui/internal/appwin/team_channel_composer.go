package appwin

import (
	"image"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type teamChannelComposer struct {
	area              *kit.TextAreaState
	send, mention     *kit.ButtonState
	menu              *kit.SuggestListState
	drafts            map[string]string
	thread, dismissed string
	direct            string
	posting           uint64
}

func newTeamChannelComposer() *teamChannelComposer {
	return &teamChannelComposer{
		area: kit.NewTextAreaState(), send: kit.NewButtonState(), mention: kit.NewButtonState(),
		menu: kit.NewSuggestListState(), drafts: make(map[string]string),
	}
}

func (c *teamChannelComposer) setThread(thread string) {
	c.drafts[c.thread] = c.area.Text()
	c.thread = thread
	c.area.SetText(c.drafts[thread])
	c.area.MoveCaretToEnd()
	c.dismissed = ""
	c.menu.Close()
}

func channelMention(task *client.TaskNode) string {
	role := strings.ToLower(channelRole(task))
	if strings.IndexFunc(role, unicode.IsSpace) >= 0 {
		return task.TaskID
	}
	return role
}

func channelMessageTarget(body string, threads []teamChannelThread) (string, string) {
	for _, word := range strings.Fields(body) {
		if mention, ok := channelMentionWord(word); ok {
			if id := resolveChannelMention(mention, threads); id != "" {
				return id, ""
			}
			return "", "Choose an agent from the mention list."
		}
	}
	return "", ""
}

func channelMentionWord(word string) (string, bool) {
	if !strings.HasPrefix(word, "@") {
		return "", false
	}
	return strings.TrimRight(strings.TrimPrefix(word, "@"), ",:;.!?)"), true
}

func resolveChannelMention(mention string, threads []teamChannelThread) string {
	var target string
	for _, thread := range threads {
		if strings.EqualFold(mention, thread.node.TaskID) || strings.EqualFold(mention, thread.id) {
			return thread.node.TaskID
		}
		if strings.EqualFold(mention, channelMention(thread.node)) {
			if target != "" {
				return ""
			}
			target = thread.node.TaskID
		}
	}
	return target
}

func trailingChannelMention(draft string) (int, string, bool) {
	start := strings.LastIndexFunc(draft, unicode.IsSpace) + 1
	if start > 0 {
		// All whitespace delimiters are complete UTF-8 runes.
		_, size := utf8.DecodeRuneInString(draft[start-1:])
		start += size - 1
	}
	query, ok := strings.CutPrefix(draft[start:], "@")
	return start, query, ok
}

func (c *teamChannelComposer) layout(gtx *layout.Context, env *kit.Env, s *screens, teamID string, view *client.TeamView, threads []teamChannelThread) layout.Dimensions {
	props := &kit.TextAreaProps{
		Placeholder: "Message the team…", EnterSubmits: true, MaxLines: 3,
		Disabled: view.VaultLocked || view.Summary.State != channelActive,
	}
	if c.direct != "" {
		props.Placeholder = "Message this agent…"
	} else {
		_, props.Error = channelMessageTarget(c.area.Text(), threads)
	}
	props.Helper = "Enter to send · Shift+Enter for a new line"
	c.syncPicker(gtx, threads, props.Disabled || c.direct != "")
	var submit bool
	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(*gtx,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			field := c.area.Measure(env, &g, props)
			ev := c.area.Layout(env, &g, props)
			submit = ev.Submitted
			if g.Focused(c.area.Tag()) {
				c.area.ScrollToCaret()
			}
			return field
		}),
		layout.Rigid(layout.Spacer{Width: theme.SpaceSM}.Layout),
		c.mentionAction(gtx, env, s, props.Disabled),
		c.sendAction(env, s, props.Disabled, &submit),
	)
	if submit && !props.Disabled {
		c.submit(s, teamID, threads)
	}
	c.layoutPicker(gtx, env, s, threads, dims.Size.X)
	return dims
}

func (c *teamChannelComposer) sendAction(env *kit.Env, s *screens, disabled bool, submit *bool) layout.FlexChild {
	return channelButton(env, s, c.send, kit.ButtonProps{
		Text:     sendLabelText,
		Variant:  kit.Primary,
		Size:     kit.SizeMD,
		Busy:     c.posting != 0,
		Disabled: disabled || strings.TrimSpace(c.area.Text()) == "",
	}, func() {
		*submit = true
	})
}

func (c *teamChannelComposer) mentionAction(gtx *layout.Context, env *kit.Env, s *screens, disabled bool) layout.FlexChild {
	return layout.Rigid(func(g layout.Context) layout.Dimensions {
		if c.direct != "" {
			return layout.Dimensions{}
		}
		return layout.Inset{Right: theme.SpaceSM}.Layout(g, func(g layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(g, channelButton(env, s, c.mention, kit.ButtonProps{Text: "@", Size: kit.SizeMD, Disabled: disabled}, func() {
				c.startMention(gtx)
			}))
		})
	})
}

func (c *teamChannelComposer) startMention(gtx *layout.Context) {
	draft := c.area.Text()
	if _, _, active := trailingChannelMention(draft); !active {
		if strings.TrimSpace(draft) == "" {
			draft = "@"
		} else {
			draft = strings.TrimRightFunc(draft, unicode.IsSpace) + " @"
		}
		c.area.SetText(draft)
		c.area.MoveCaretToEnd()
	}
	c.dismissed = ""
	c.menu.Open()
	gtx.Execute(key.FocusCmd{Tag: c.area.Tag()})
}

func (c *teamChannelComposer) submit(s *screens, teamID string, threads []teamChannelThread) {
	body := strings.TrimSpace(c.area.Text())
	to, problem := c.direct, ""
	if to == "" {
		to, problem = channelMessageTarget(body, threads)
	}
	if body == "" || problem != "" {
		c.area.MarkSubmitted()
		return
	}
	if c.posting != 0 {
		return
	}
	// The draft stays until the host accepts the post (see finishTeamPost).
	if c.posting = s.postTeamMessage(c, teamID, c.thread, to, body); c.posting != 0 {
		c.menu.Close()
	}
}

func channelSuggestions(query string, threads []teamChannelThread) []kit.SuggestItem {
	items := make([]kit.SuggestItem, 0, len(threads))
	for _, thread := range threads {
		n := thread.node
		items = append(items, kit.SuggestItem{
			ID: n.TaskID, Label: channelRole(n), Secondary: n.Goal,
			Icon: kit.IconUsers, Disabled: !channelCanReceive(n.State),
		})
	}
	return kit.DefaultComboboxFilter(query, items)
}

func channelCanReceive(state string) bool {
	switch state {
	case teamStateDone, channelFailed, "stopped", "cancelled":
		return false
	default:
		return true
	}
}

func (c *teamChannelComposer) syncPicker(gtx *layout.Context, threads []teamChannelThread, disabled bool) {
	draft := c.area.Text()
	_, query, mention := trailingChannelMention(draft)
	if disabled || !mention || !gtx.Focused(c.area.Tag()) || draft == c.dismissed {
		c.menu.Close()
		return
	}
	c.menu.Open()
	c.pickerKeys(gtx, channelSuggestions(query, threads), draft)
}

func (c *teamChannelComposer) pickerKeys(gtx *layout.Context, items []kit.SuggestItem, draft string) {
	tag := c.area.Tag()
	filters := []event.Filter{
		key.Filter{Focus: tag, Name: key.NameUpArrow},
		key.Filter{Focus: tag, Name: key.NameDownArrow},
		key.Filter{Focus: tag, Name: key.NameReturn},
		key.Filter{Focus: tag, Name: key.NameEnter},
		key.Filter{Focus: tag, Name: key.NameEscape},
	}
	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		ke, isKey := e.(key.Event)
		if !isKey {
			continue
		}
		if id, closed, _ := c.menu.HandleKey(ke, items); id != "" {
			c.pick(id)
		} else if closed {
			c.dismissed = draft
		}
	}
}

func (c *teamChannelComposer) layoutPicker(gtx *layout.Context, env *kit.Env, s *screens, threads []teamChannelThread, width int) {
	_, query, _ := trailingChannelMention(c.area.Text())
	res, err := c.menu.Layout(env, gtx, &kit.SuggestProps{
		Items:  channelSuggestions(query, threads),
		Anchor: image.Rect(0, 0, width, gtx.Dp(theme.ControlMD)),
		Query:  query, EmptyText: "No agents", NoResText: "No matching agent",
	})
	s.fail(err)
	if res.Chosen != "" {
		c.pick(res.Chosen)
	} else if res.Closed {
		c.dismissed = c.area.Text()
	}
}

func (c *teamChannelComposer) pick(id string) {
	draft := c.area.Text()
	start, _, mention := trailingChannelMention(draft)
	if mention {
		c.area.SetText(draft[:start] + "@" + id + " ")
		c.area.MoveCaretToEnd()
	}
	c.menu.Close()
}
