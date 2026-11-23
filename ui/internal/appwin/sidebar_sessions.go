package appwin

import (
	"fmt"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
)

// sessionRows builds one kit row per session of the given list; the row's
// click opens it. A row reads icon, vendor name, then the state as a status
// dot plus muted text: the text carries the state, the dot's tone only
// supports it.
func (s *screens) sessionRows(env *kit.Env, sessions []session.Session) []layout.FlexChild {
	rows := make([]layout.FlexChild, 0, len(sessions))
	for _, sess := range sessions {
		row, ok := s.rows[sess.ID]
		if !ok {
			row = kit.NewListRowState()
			row.Enter()
			s.rows[sess.ID] = row
		}
		selected := s.grid.rail == railChat && s.grid.mainRef().ID == sess.ID && s.grid.mainRef().Kind == KindSession
		pending := s.inbox.PendingCountForSession(sess.ID)
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			text, tone := sessionState(sess)
			props := &kit.ListRowProps{
				Icon:          kit.IconBot,
				Label:         sessionName(sess),
				Secondary:     text,
				SecondaryDot:  true,
				SecondaryTone: tone,
				Selected:      selected,
			}
			if pending > 0 {
				props.StateLabel = fmt.Sprintf("%d pending", pending)
				props.StateTone = kit.ToneWarning
			}
			dims, ev := row.Layout(env, &local, props)
			if ev.Clicked {
				s.openSession(sess.ID)
			}
			return dims
		}))
	}
	return rows
}

// sessionName names a session row: the vendor, or "session" when the list
// read has not named one yet.
func sessionName(sess session.Session) string {
	if sess.Vendor == "" {
		return "session"
	}
	return sess.Vendor
}

// sessionState is the row's state text and the tone of its status dot. The
// vendor states map to what the user needs to do: ready is quiet success,
// running is info, awaiting_approval reads "needs you", and the states that
// leave a turn's outcome unknown or blocked are warnings. The text is
// always shown, so the tone is never the only carrier of the state.
func sessionState(sess session.Session) (string, kit.ListRowTone) {
	text, tone := sess.State, kit.ToneNeutral
	switch sess.State {
	case stateReady:
		tone = kit.ToneSuccess
	case stateRunning, stateStarting:
		tone = kit.ToneInfo
	case stateAwaitingApproval:
		text, tone = "needs you", kit.ToneWarning
	case stateWaiting, stateDetached:
		tone = kit.ToneWarning
	case "failed":
		tone = kit.ToneDanger
	}
	if sess.WaitingReason != "" {
		text += " · " + sess.WaitingReason
	}
	return text, tone
}
