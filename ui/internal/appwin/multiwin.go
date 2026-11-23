package appwin

import (
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// windowUpdates is one open window's connection to the multi-window
// coordinator: its delivery queue, wakeup callback, and the dynamic topics it
// currently subscribes to.
type windowUpdates struct {
	queue      *updateQueue
	invalidate func()
	topics     map[string]bool
}

// multiWindow coordinates all open app windows over the single shared
// hostconn.Manager (desktop-app §5.3, DA-E10): it fans out deliveries to every
// window, reference-counts dynamic topic interests across windows, spawns
// secondary windows on startup restore, and tracks window count for close
// semantics (DA-07, host-api §5.2).
type multiWindow struct {
	m      *hostconn.Manager
	openFn func(windowID string, record *client.AppStateWindow, doc *LayoutDoc)
	newID  func() string

	mu       sync.Mutex
	windows  map[*windowUpdates]struct{}
	topics   map[string]int
	restored bool
	idSeq    atomic.Uint64
	idSeed   uint64
}

// newMultiWindow builds a coordinator over m and an optional window spawner.
func newMultiWindow(m *hostconn.Manager, openFn func(windowID string, record *client.AppStateWindow, doc *LayoutDoc)) *multiWindow {
	return &multiWindow{
		m:       m,
		openFn:  openFn,
		windows: map[*windowUpdates]struct{}{},
		topics:  map[string]int{},
		idSeed:  0x46756775654d5731,
	}
}

// register adds a window to the active set.
func (c *multiWindow) register(w *windowUpdates) {
	if w == nil {
		return
	}
	c.mu.Lock()
	c.windows[w] = struct{}{}
	c.mu.Unlock()
}

// unregister removes w from the active set and releases its topic interests.
func (c *multiWindow) unregister(w *windowUpdates) {
	if w == nil {
		return
	}
	c.mu.Lock()
	removed := c.removeLocked(w)
	c.mu.Unlock()
	for _, topic := range removed {
		if c.m != nil {
			c.m.RemoveTopic(topic)
		}
	}
}

// beginClose decides whether w is the last remaining window. Secondary windows
// are unregistered atomically so two concurrent closes cannot both treat
// themselves as secondary; the last window stays registered until it actually
// exits in case requestWindowClose cancels the close.
func (c *multiWindow) beginClose(w *windowUpdates) (isLast bool) {
	c.mu.Lock()
	if len(c.windows) <= 1 {
		c.mu.Unlock()
		return true
	}
	removed := c.removeLocked(w)
	c.mu.Unlock()
	for _, topic := range removed {
		if c.m != nil {
			c.m.RemoveTopic(topic)
		}
	}
	return false
}

// removeLocked deletes w from c.windows and returns any topics whose global
// reference count dropped to zero. Caller must hold c.mu.
func (c *multiWindow) removeLocked(w *windowUpdates) []string {
	if w == nil {
		return nil
	}
	delete(c.windows, w)
	var removed []string
	for topic := range w.topics {
		c.topics[topic]--
		if c.topics[topic] <= 0 {
			delete(c.topics, topic)
			removed = append(removed, topic)
		}
	}
	w.topics = nil
	return removed
}

// setTopic updates one window's interest in a dynamic topic and forwards
// 0<->1 reference-count transitions to the shared hostconn.Manager.
func (c *multiWindow) setTopic(w *windowUpdates, topic string, active bool) {
	if w == nil || topic == "" {
		return
	}
	add, remove := c.updateTopicRef(w, topic, active)
	if c.m == nil {
		return
	}
	if add {
		c.m.AddTopic(topic)
	} else if remove {
		c.m.RemoveTopic(topic)
	}
}

func (c *multiWindow) updateTopicRef(w *windowUpdates, topic string, active bool) (add, remove bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w.topics == nil {
		w.topics = map[string]bool{}
	}
	if w.topics[topic] == active {
		return false, false
	}
	if active {
		w.topics[topic] = true
		c.topics[topic]++
		return c.topics[topic] == 1, false
	}
	delete(w.topics, topic)
	c.topics[topic]--
	if c.topics[topic] <= 0 {
		delete(c.topics, topic)
		return false, true
	}
	return false, false
}

// deliver fans one hostconn.Update out to every active window and spawns any
// additional stored windows when the initial appstate snapshot arrives.
func (c *multiWindow) deliver(u hostconn.Update) {
	var secondary []client.AppStateWindow
	c.mu.Lock()
	targets := slices.Collect(maps.Keys(c.windows))
	if u.Topic == topicAppState && u.Snapshot != nil && !c.restored {
		c.restored = true
		if snap, err := client.DecodeAppStateSnapshot(u.Snapshot.State); err == nil {
			sorted := sortAppStateWindows(snap.Windows)
			if len(sorted) > 1 {
				secondary = sorted[1:]
			}
		}
	}
	c.mu.Unlock()

	for _, w := range targets {
		if w.queue != nil {
			w.queue.push(u)
		}
		if w.invalidate != nil {
			w.invalidate()
		}
	}
	if c.openFn != nil {
		for i := range secondary {
			rec := secondary[i]
			c.openFn(rec.WindowID, &rec, nil)
		}
	}
}

// openNew spawns a new secondary window for doc with a fresh UUID.
func (c *multiWindow) openNew(doc LayoutDoc) error {
	id := c.nextWindowID()
	if c.openFn != nil {
		c.openFn(id, nil, &doc)
	}
	return nil
}

func (c *multiWindow) nextWindowID() string {
	if c.newID != nil {
		return c.newID()
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], c.idSeed)
	binary.BigEndian.PutUint64(b[8:], c.idSeq.Add(1))
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// openSlotWindow consumes a pending "Open in new window" request, invokes open
// with the new window's initial layout, and removes the popped slot from the
// source window's grid.
func (s *screens) openSlotWindow(open func(LayoutDoc) error) bool {
	if s.grid == nil || s.grid.newWindow == nil {
		return false
	}
	req := s.grid.newWindow
	s.grid.newWindow = nil
	if open == nil {
		return false
	}
	if err := open(req.Doc); err != nil {
		s.fail(err)
		return false
	}
	s.grid.edited(CloseSlot(s.grid.doc, req.Pos))
	s.trackTopics()
	return true
}

// installNewLayout initializes a newly spawned secondary window with its
// windowID and layout document, marking it dirty so it persists immediately.
func installNewLayout(sc *screens, windowID string, doc LayoutDoc) {
	g := sc.grid
	g.windowID = windowID
	g.rev = 0
	g.loaded = true
	g.higher = false
	g.doc = cloneLayout(doc)
	g.dirty = true
	g.saveAt = time.Time{}
	g.syncFromDoc()
	sc.trackTopics()
}

func runWindow(ctx context.Context, m *hostconn.Manager, coord *multiWindow, windowID string, record *client.AppStateWindow, doc *LayoutDoc) error {
	winCtx, winCancel := context.WithCancel(ctx)
	scale, screenW, screenH := workArea()
	initW, initH := ClampDp(requestedW, requestedH, scale, screenW, screenH)
	w := new(app.Window)
	w.Option(app.Title("Fugue"))
	w.Option(app.Size(unit.Dp(initW), unit.Dp(initH)))

	q := &updateQueue{}
	winReg := &windowUpdates{queue: q, invalidate: w.Invalidate}
	coord.register(winReg)
	defer coord.unregister(winReg)

	workers := startAppWorkers(winCtx, m, w)
	defer workers.stop()
	closerDone := startCloser(winCtx, w)
	defer func() {
		winCancel()
		<-closerDone
	}()

	shaper, err := fonts.Shaper()
	if err != nil {
		return fmt.Errorf("build the text shaper: %w", err)
	}
	light, dark := theme.Light(shaper), theme.Dark(shaper)
	env := newEnv(shaper, &light)
	attach(env, w)
	sc := newWindowScreens(m, w, workers, coord, winReg)
	applyInitialWindowLayout(m, sc, q, windowID, record, doc)
	return eventLoop(winCtx, w, env, sc, q, coord, winReg, &light, &dark)
}

func newWindowScreens(m *hostconn.Manager, w *app.Window, workers appWorkers, coord *multiWindow, winReg *windowUpdates) *screens {
	sc := newScreens(m, workers.fetcher, workers.poster, workers.wsMut)
	sc.setTopic = func(topic string, active bool) {
		coord.setTopic(winReg, topic, active)
	}
	sc.vaultWorker = workers.vaultW
	sc.inboxWorker = workers.inboxW
	sc.accountsWorker = workers.accountsW
	sc.settingsWorker = workers.settingsW
	sc.secretsWorker = workers.secretsW
	sc.viewerWorker = workers.viewerW
	sc.layoutWorker = workers.layoutW
	sc.grid.applyGeometry = func(widthDp, heightDp int) {
		if widthDp > 0 && heightDp > 0 {
			w.Option(app.Size(unit.Dp(widthDp), unit.Dp(heightDp)))
		}
	}
	return sc
}

func applyInitialWindowLayout(m *hostconn.Manager, sc *screens, q *updateQueue, windowID string, record *client.AppStateWindow, doc *LayoutDoc) {
	if record != nil {
		sc.applyWindowRecord(record)
		sc.trackTopics()
	} else if doc != nil {
		installNewLayout(sc, windowID, *doc)
		sc.grid.maybeSave(sc.layoutWorker)
	}
	if (record != nil || doc != nil) && m != nil {
		if st, _ := m.State(); st == hostconn.StateConnected {
			q.push(hostconn.Update{State: hostconn.StateConnected})
		}
	}
}
