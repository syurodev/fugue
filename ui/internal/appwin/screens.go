package appwin

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/transcriptview"
)

// The topic name pieces the screens route on; the constants mirror the
// host's topic owners.
const (
	topicVendorSessions = "vendor-sessions"
	topicTranscript     = "transcript:"
	topicTranscriptDlt  = "transcript-delta:"
)

// screens is the app's read side (F10.4a): the sidebar's session list, the
// open session's transcript model, and the transcript view's state. It
// changes only on the window goroutine, which applies every delivered update
// here.
type screens struct {
	m       *hostconn.Manager
	fetcher *session.Fetcher

	sidebar     *session.Sidebar
	transcripts map[string]*session.Transcript
	// open is the open session's id, empty when none is.
	open string

	view   *transcriptview.State
	clicks map[string]*widget.Clickable
	// pendingPage and pendingOlder guard one read per trigger, and scrolled
	// makes paging older a user-driven act: it arms after the first scroll
	// and resets when the session changes.
	pendingPage  map[string]bool
	pendingOlder map[string]bool
	scrolled     bool
	// lastErr is the last read or payload failure the screens could not act
	// on, shown under the transcript.
	lastErr string
}

// newScreens builds the screens over the manager and its fetcher.
func newScreens(m *hostconn.Manager, fetcher *session.Fetcher) *screens {
	return &screens{
		m:            m,
		fetcher:      fetcher,
		sidebar:      session.NewSidebar(),
		transcripts:  map[string]*session.Transcript{},
		view:         transcriptview.NewState(),
		clicks:       map[string]*widget.Clickable{},
		pendingPage:  map[string]bool{},
		pendingOlder: map[string]bool{},
	}
}

// update folds one frame's deliveries into the screens.
func (s *screens) update(updates []hostconn.Update, results []session.Result) {
	for _, u := range updates {
		s.applyUpdate(u)
	}
	for _, r := range results {
		s.applyResult(r)
	}
}

// applyUpdate routes one topic delivery or connection-state change.
func (s *screens) applyUpdate(u hostconn.Update) {
	switch {
	case u.State == hostconn.StateConnected:
		s.reconnected()
	case u.Topic == topicVendorSessions && u.Snapshot != nil:
		s.fail(s.sidebar.ApplySnapshot(u.Snapshot.State))
	case u.Topic == topicVendorSessions && u.Event != nil:
		s.applySessionEvent(u.Event.Type, u.Event.Payload)
	case u.Topic == topicTranscriptDlt+s.open && u.Event != nil:
		s.applyDelta(u.Event.Payload)
	case u.Topic == topicTranscript+s.open:
		s.applyTranscriptDelivery(u)
	}
}

// reconnected arms the reads a fresh connection carries: the session list,
// the open session's topics, and its newest page. A request dropped while the
// host was down re-arms here.
func (s *screens) reconnected() {
	delete(s.pendingPage, s.open)
	delete(s.pendingOlder, s.open)
	s.fetcher.RequestSessions()
	if s.open != "" {
		s.m.AddTopic(topicTranscript + s.open)
		s.m.AddTopic(topicTranscriptDlt + s.open)
		s.requestNewestPage(s.open)
	}
}

// applySessionEvent applies one vendor-sessions event. A created event
// carries no vendor or timestamps; the list read names them.
func (s *screens) applySessionEvent(eventType string, payload []byte) {
	if err := s.sidebar.ApplyEvent(eventType, payload); err != nil {
		s.fail(err)
		return
	}
	if eventType == session.EventSessionCreated {
		s.fetcher.RequestSessions()
	}
}

// applyDelta applies one transcript-delta delivery to the open session.
func (s *screens) applyDelta(payload []byte) {
	if t := s.transcripts[s.open]; t != nil {
		s.fail(t.ApplyDelta(payload))
	}
}

// applyTranscriptDelivery applies one durable transcript topic delivery.
func (s *screens) applyTranscriptDelivery(u hostconn.Update) {
	t := s.transcripts[s.open]
	if t == nil {
		return
	}
	if u.Snapshot != nil {
		s.fail(t.ApplySnapshot(u.Snapshot.State))
		s.requestNewestPage(s.open)
		return
	}
	if u.Event != nil {
		s.fail(t.ApplyEvent(u.Event.Type, u.Event.Sequence, u.Event.Payload))
	}
}

// applyResult folds one fetcher read into the screens.
func (s *screens) applyResult(r session.Result) {
	switch r.Kind {
	case session.ResultSessions:
		if r.Err != nil {
			s.fail(fmt.Errorf("read the session list: %w", r.Err))
			return
		}
		s.sidebar.ApplyList(r.Sessions)
	case session.ResultPage:
		t := s.transcripts[r.Session]
		if t == nil {
			return
		}
		if r.Err != nil {
			s.fail(fmt.Errorf("read the transcript page: %w", r.Err))
			return
		}
		if r.Older {
			t.ApplyOlderPage(r.Items)
			s.pendingOlder[r.Session] = false
		} else {
			t.ApplyNewestPage(r.Items)
			s.pendingPage[r.Session] = false
		}
	}
}

// openSession opens a session's transcript: its topics join the connection,
// and its newest page is read if the model has none.
func (s *screens) openSession(id string) {
	if s.open == id {
		return
	}
	if s.open != "" {
		s.m.RemoveTopic(topicTranscript + s.open)
		s.m.RemoveTopic(topicTranscriptDlt + s.open)
	}
	s.open, s.scrolled = id, false
	s.m.AddTopic(topicTranscript + id)
	s.m.AddTopic(topicTranscriptDlt + id)
	if _, ok := s.transcripts[id]; !ok {
		s.transcripts[id] = session.NewTranscript(id)
	}
	s.requestNewestPage(id)
}

// requestNewestPage reads the newest page once per trigger.
func (s *screens) requestNewestPage(id string) {
	if s.pendingPage[id] {
		return
	}
	s.pendingPage[id] = true
	s.fetcher.RequestPage(id, 0)
}

// maybePageOlder reads the next older page when the reader has scrolled back
// to the top: paging older is on demand, never a background crawl.
func (s *screens) maybePageOlder() {
	t := s.transcripts[s.open]
	if t == nil || !s.scrolled || s.pendingOlder[s.open] || !s.view.AtTop() {
		return
	}
	if before, ok := t.OlderCursor(); ok {
		s.pendingOlder[s.open] = true
		s.fetcher.RequestPage(s.open, before)
	}
}

// fail records a failure the screens could not act on.
func (s *screens) fail(err error) {
	if err != nil {
		s.lastErr = err.Error()
	}
}

// layout draws the sidebar and the transcript pane.
func (s *screens) layout(gtx layout.Context, th theme.Theme) {
	layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return s.layoutSidebar(gtx, th)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return s.layoutTranscript(gtx, th)
		}),
	)
}

// layoutSidebar draws the session list; a row click opens the session.
func (s *screens) layoutSidebar(gtx layout.Context, th theme.Theme) layout.Dimensions {
	mt := th.Material()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(mt, unit.Sp(14), "Sessions")
			l.Color = th.Chrome().FgMuted
			return l.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, s.sessionRows(gtx, th, mt)...)
		}),
	)
}

// sessionRows builds one clickable row per session.
func (s *screens) sessionRows(gtx layout.Context, th theme.Theme, mt *material.Theme) []layout.FlexChild {
	rows := make([]layout.FlexChild, 0, len(s.sidebar.List()))
	for _, sess := range s.sidebar.List() {
		click, ok := s.clicks[sess.ID]
		if !ok {
			click = new(widget.Clickable)
			s.clicks[sess.ID] = click
		}
		if click.Clicked(gtx) {
			s.openSession(sess.ID)
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				label := sess.Vendor
				if label == "" {
					label = "session"
				}
				label += " · " + sess.State
				if sess.WaitingReason != "" {
					label += " · " + sess.WaitingReason
				}
				l := material.Label(mt, unit.Sp(13), label)
				l.Color = th.Chrome().Fg
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, l.Layout)
			})
		}))
	}
	return rows
}

// layoutTranscript draws the open session's transcript, read-only.
func (s *screens) layoutTranscript(gtx layout.Context, th theme.Theme) layout.Dimensions {
	mt := th.Material()
	var items []*session.Item
	if t := s.transcripts[s.open]; t != nil {
		items = t.Items()
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			paint.FillShape(gtx.Ops, th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
			if len(items) == 0 && s.open == "" {
				l := material.Label(mt, unit.Sp(14), "Select a session")
				l.Color = th.Chrome().FgMuted
				return layout.Center.Layout(gtx, l.Layout)
			}
			dims := transcriptview.Layout(&gtx, &th, s.view, items)
			s.trackScroll()
			s.maybePageOlder()
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if s.lastErr == "" {
				return layout.Dimensions{}
			}
			l := material.Label(mt, unit.Sp(12), s.lastErr)
			l.Color = th.Status().Danger
			return l.Layout(gtx)
		}),
	)
}

// trackScroll marks that the reader has scrolled, which arms paging older.
func (s *screens) trackScroll() {
	if s.view.Offset() > 0 {
		s.scrolled = true
	}
}
