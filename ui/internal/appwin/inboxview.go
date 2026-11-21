package appwin

import (
	"fmt"
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const codeStaleIncarnation = "interaction.stale_incarnation"

// inboxView owns the approvals inbox pane (F10.5b, desktop-app §5).
// Until F10.20's slot grid lands, it occupies the right pane when selected
// from the sidebar or toggled with Ctrl/Cmd+Shift+I.
type inboxView struct {
	active, focusedPin inbox.Pin
	forms              map[inbox.Pin]*cardFormState
	queueRows          map[inbox.Pin]*kit.ListRowState
	empty              *kit.EmptyStateState
	backBtn            *kit.ButtonState
	countTag           *kit.TagState
	card               *kit.CardState
	scroll             *kit.ScrollAreaState
	errBanner          *kit.BannerState
}

func newInboxView() *inboxView {
	return &inboxView{
		forms: map[inbox.Pin]*cardFormState{}, queueRows: map[inbox.Pin]*kit.ListRowState{},
		empty: kit.NewEmptyStateState(), backBtn: kit.NewButtonState(), countTag: kit.NewTagState(),
		card: kit.NewCardState(), scroll: kit.NewScrollAreaState(), errBanner: kit.NewBannerState(),
	}
}

func (v *inboxView) reconcile(m *inbox.Model) {
	items := m.Items()
	live := make(map[inbox.Pin]bool, len(items))
	for i := range items {
		live[inbox.Pin{RequestID: items[i].RequestID, Incarnation: items[i].Incarnation}] = true
	}
	pruneKnownMap(v.forms, live)
	pruneKnownMap(v.queueRows, live)
	if len(items) == 0 {
		v.active, v.focusedPin = inbox.Pin{}, inbox.Pin{}
		return
	}
	if !live[v.active] {
		v.active = inbox.Pin{RequestID: items[0].RequestID, Incarnation: items[0].Incarnation}
	}
}

func pruneKnownMap[V any](m map[inbox.Pin]V, live map[inbox.Pin]bool) {
	for k := range m {
		if !live[k] {
			delete(m, k)
		}
	}
}

func inboxErrorMessage(code string, err error) string {
	switch code {
	case "interaction.already_answered":
		return "This request was already answered elsewhere."
	case "interaction.not_found":
		return "This request is no longer pending."
	case codeStaleIncarnation:
		return "Host restarted; refreshing pending requests."
	case "interaction.option_not_offered":
		return "That option is no longer valid for this request."
	case "interaction.text_too_large":
		return "Text exceeds the host size limit."
	default:
		return err.Error()
	}
}

func (v *inboxView) applied(res *inbox.Result, m *inbox.Model, w *inbox.Worker) {
	if form := v.forms[res.Pin]; form != nil {
		form.busy, form.lastErr = false, ""
		if res.Err != nil {
			form.lastErr = inboxErrorMessage(res.Code, res.Err)
		}
	}
	if res.Err == nil || res.Code == "interaction.already_answered" || res.Code == "interaction.not_found" || res.Code == codeStaleIncarnation {
		m.Remove(res.Pin)
		delete(v.forms, res.Pin)
		if res.Code == codeStaleIncarnation && w != nil {
			req := inbox.Request{Kind: inbox.OpListPending}
			w.Do(&req)
		}
		v.reconcile(m)
	}
}

func (s *screens) applyInteractionDelivery(u hostconn.Update) {
	if u.Snapshot != nil {
		s.fail(s.inbox.ApplyTopicSnapshot(u.Snapshot.Incarnation, u.Snapshot.Sequence, u.Snapshot.State))
	} else if u.Event != nil {
		s.fail(s.inbox.ApplyTopicEvent(u.Event.Sequence, u.Event.Type, u.Event.Payload))
	}
	s.inboxView.reconcile(s.inbox)
}

func (s *screens) applyInboxResults(results []inbox.Result) {
	for i := range results {
		res := &results[i]
		if res.Kind == inbox.OpListPending {
			if res.Err != nil {
				s.fail(fmt.Errorf("list pending approvals: %w", res.Err))
				continue
			}
			s.inbox.ApplyListPending(res.Incarnation, res.List)
			s.inboxView.reconcile(s.inbox)
			continue
		}
		s.inboxView.applied(res, s.inbox, s.inboxWorker)
	}
}

func (s *screens) toggleInboxShortcut(gtx *layout.Context) {
	for {
		ke, ok := gtx.Event(key.Filter{Required: key.ModShortcut | key.ModShift, Name: key.Name("I")})
		if !ok {
			return
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			s.inboxOpen = !s.inboxOpen
			if s.inboxOpen {
				s.accountsOpen = false
			}
		}
	}
}

func (v *inboxView) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	items := sc.inbox.Items()
	return layout.Inset{Top: theme.SpaceMD, Bottom: theme.SpaceMD, Left: theme.SpaceLG, Right: theme.SpaceLG}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutHeader(gtx, env, sc, len(items)) }),
			spacerH(theme.SpaceMD),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return v.layoutBody(&local, env, sc, items)
			}),
		)
	})
}

func (v *inboxView) layoutHeader(gtx *layout.Context, env *kit.Env, sc *screens, count int) layout.Dimensions {
	status := theme.StatusInfo
	if count > 0 {
		status = theme.StatusWarning
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		rigidLabel(env, theme.TypeTitle(), env.Th.Chrome().Fg, "Approvals inbox"),
		spacerW(theme.SpaceSM),
		rigidTag(env, sc.fail, v.countTag, kit.TagProps{Text: fmt.Sprintf("%d pending", count), Variant: kit.TagStatus, Status: status}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
		}),
		rigidInboxButton(env, sc.fail, v.backBtn, kit.ButtonProps{Text: "Back to chat", Variant: kit.Ghost, Size: kit.SizeSM, Disabled: sc.open == ""}, func() {
			sc.inboxOpen = false
		}),
	)
}

func (v *inboxView) layoutBody(gtx *layout.Context, env *kit.Env, sc *screens, items []client.PendingInteraction) layout.Dimensions {
	if len(items) == 0 {
		v.focusedPin = inbox.Pin{}
		dims, _, err := v.empty.Layout(env, gtx, &kit.EmptyStateProps{
			Icon: kit.IconCheck, Title: "No pending approvals",
			Description: "Approval requests and agent questions from sessions will appear here.",
		})
		sc.fail(err)
		return dims
	}
	v.reconcile(sc.inbox)
	activeReq, ok := sc.inbox.Lookup(v.active)
	if !ok {
		activeReq = items[0]
		v.active = inbox.Pin{RequestID: activeReq.RequestID, Incarnation: activeReq.Incarnation}
	}
	var children []layout.FlexChild
	if len(items) > 1 {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutQueue(gtx, env, items) }), spacerH(theme.SpaceMD))
	}
	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return v.layoutActiveCard(&local, env, sc, &activeReq)
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (v *inboxView) layoutQueue(gtx *layout.Context, env *kit.Env, items []client.PendingInteraction) layout.Dimensions {
	rows := make([]layout.FlexChild, 0, len(items))
	for i := range items {
		item := items[i]
		pin := inbox.Pin{RequestID: item.RequestID, Incarnation: item.Incarnation}
		row := ensureOptionRow(v.queueRows, pin)
		title := item.Tool
		if item.Kind == client.InteractionKindQuestion && title == "" {
			title = "Agent question"
		}
		rows = append(rows, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, ev := row.Layout(env, gtx, &kit.ListRowProps{
				Label: title, Secondary: item.Summary, StateLabel: item.Kind,
				StateTone: kit.ToneWarning, Trailing: item.EnvTag, Selected: v.active == pin,
			})
			if ev.Clicked || ev.Opened {
				v.active = pin
			}
			return dims
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, rows...)
}

func spacerH(h unit.Dp) layout.FlexChild { return layout.Rigid(layout.Spacer{Height: h}.Layout) }
func spacerW(w unit.Dp) layout.FlexChild { return layout.Rigid(layout.Spacer{Width: w}.Layout) }
