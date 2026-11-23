package appwin

import (
	"fmt"

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

// inboxView owns the approvals inbox slot (F10.5b, desktop-app §5): the
// shell's grid opens it in the nearest empty slot, the sidebar row and the
// Ctrl/Cmd+Shift+I shortcut toggle it.
type inboxView struct {
	active, focusedPin inbox.Pin
	proposalTeam       string
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

func (v *inboxView) clearSecretInputs() {
	for _, f := range v.forms {
		f.clearSecrets()
	}
}

func (v *inboxView) reconcile(m *inbox.Model) {
	items := m.Items()
	live := make(map[inbox.Pin]bool, len(items))
	for i := range items {
		live[inbox.Pin{RequestID: items[i].RequestID, Incarnation: items[i].Incarnation}] = true
	}
	for k, f := range v.forms {
		if !live[k] {
			f.clearSecrets()
			delete(v.forms, k)
		}
	}
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
		if f := v.forms[res.Pin]; f != nil {
			f.clearSecrets()
		}
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
	s.listTeams()
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

func (v *inboxView) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	sc.selectProposalRequest()
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
	return flowHeader(gtx,
		flowTitle(env, gtx, theme.TypeTitle(), env.Th.Chrome().Fg, "Approvals inbox"),
		flowTag(env, gtx, sc.fail, v.countTag, kit.TagProps{Text: fmt.Sprintf("%d pending", count), Variant: kit.TagStatus, Status: status}),
		atEnd(flowButton(env, gtx, sc.fail, v.backBtn, kit.ButtonProps{Text: labelBackToChat, Variant: kit.Ghost, Size: kit.SizeSM}, func() {
			sc.grid.closeSlotOf(KindInbox)
		})),
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
