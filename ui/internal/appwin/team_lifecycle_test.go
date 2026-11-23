package appwin

import (
	"context"
	"errors"
	"image"
	"sync"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/team"
)

var errLifecycle = errors.New("lifecycle host failure")

const keptTeam = "keep"

type topicRecorder struct {
	mu      sync.Mutex
	adds    map[string]int
	removes map[string]int
}

func newTopicRecorder() *topicRecorder {
	return &topicRecorder{adds: map[string]int{}, removes: map[string]int{}}
}

func (r *topicRecorder) AddTopic(topic string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adds[topic]++
}

func (r *topicRecorder) RemoveTopic(topic string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removes[topic]++
}

type lifecycleHost struct {
	team.Client
	mu    sync.Mutex
	posts int
	fail  bool
}

func (h *lifecycleHost) PostChannelMessage(context.Context, *client.PostChannelMessageInput) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.posts++
	if h.fail {
		return 0, errLifecycle
	}
	return 1, nil
}

func (*lifecycleHost) GetTeam(context.Context, *client.GetTeamInput) (client.TeamView, error) {
	return client.TeamView{}, errLifecycle
}

func (h *lifecycleHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.posts
}

func startLifecycleWorker(t *testing.T, sc *screens, host team.Client) chan struct{} {
	t.Helper()
	wake := make(chan struct{}, 16)
	w := team.Start(t.Context(), func() team.Client { return host }, func() { wake <- struct{}{} }, func() string { return "id" })
	t.Cleanup(w.Stop)
	sc.teamWorker = w
	return wake
}

func awaitWake(t *testing.T, wake chan struct{}) {
	t.Helper()
	<-wake
}

func TestTeamTopicReleasedWhenNoLongerDisplayed(t *testing.T) {
	t.Parallel()
	sc := teamShotScreens(t, tabChannel)
	startLifecycleWorker(t, sc, nil)
	rec := newTopicRecorder()
	sc.teamTopicPort = rec
	id := sc.teamSummaries[0].TeamID
	topic := client.TopicTeamChannel + id
	h := newFrameRunner(t)
	size := image.Pt(900, 600)
	show := func(g *layout.Context) {
		sc.layoutTeamBoard(g, h.env, teamBoardKey{team: id, main: true})
	}
	for range 2 {
		h.frame(t, size, show)
		sc.releaseTeamTopics()
	}
	if rec.adds[topic] != 1 || rec.removes[topic] != 0 {
		t.Fatalf("displayed team: adds=%d removes=%d, want 1 and 0", rec.adds[topic], rec.removes[topic])
	}
	for range 2 {
		h.frame(t, size, func(*layout.Context) {})
		sc.releaseTeamTopics()
	}
	if rec.removes[topic] != 1 || sc.teamTopics[id] {
		t.Fatalf("hidden team: removes=%d, tracked=%v, want 1 and false", rec.removes[topic], sc.teamTopics[id])
	}
	h.frame(t, size, show)
	sc.releaseTeamTopics()
	if rec.adds[topic] != 2 || !sc.teamPending[team.Request{Op: team.Read, Team: id, Epoch: sc.teamEpoch}] {
		t.Fatalf("redisplay: adds=%d, refresh queued=%v", rec.adds[topic], sc.teamPending)
	}
}

func TestTeamPostKeepsDraftUntilHostAccepts(t *testing.T) {
	t.Parallel()
	sc := teamShotScreens(t, tabChannel)
	host := &lifecycleHost{fail: true}
	wake := startLifecycleWorker(t, sc, host)
	id := sc.teamSummaries[0].TeamID
	c := newTeamChannelComposer()
	c.area.SetText("ship it")
	c.submit(sc, id, nil)
	c.submit(sc, id, nil)
	if c.posting == 0 || c.area.Text() != "ship it" {
		t.Fatalf("in flight: posting=%d text=%q", c.posting, c.area.Text())
	}
	awaitWake(t, wake)
	sc.drainTeamResults()
	if host.count() != 1 {
		t.Fatalf("second Send while in flight reached the host: %d posts", host.count())
	}
	if c.posting != 0 || c.area.Text() != "ship it" || sc.lastErr == "" {
		t.Fatalf("failed post: posting=%d text=%q err=%q", c.posting, c.area.Text(), sc.lastErr)
	}
	host.mu.Lock()
	host.fail = false
	host.mu.Unlock()
	c.submit(sc, id, nil)
	awaitWake(t, wake)
	sc.drainTeamResults()
	if c.posting != 0 || c.area.Text() != "" || host.count() != 2 {
		t.Fatalf("accepted post: posting=%d text=%q posts=%d", c.posting, c.area.Text(), host.count())
	}
}

func TestTeamRetryRequeuesFailedRead(t *testing.T) {
	t.Parallel()
	sc := teamShotScreens(t, tabChannel)
	startLifecycleWorker(t, sc, nil)
	sc.teamTopicPort = newTopicRecorder()
	id := sc.teamSummaries[0].TeamID
	delete(sc.teamViews, id)
	read := team.Request{Op: team.Read, Team: id, Epoch: sc.teamEpoch}
	sc.applyTeamResult(&team.Result{Op: team.Read, Team: id, Epoch: sc.teamEpoch, Err: errLifecycle})
	if !sc.teamFailedReads[read] {
		t.Fatal("failed read was not recorded")
	}
	key := teamBoardKey{team: id, main: true}
	h := newFrameRunner(t)
	size := image.Pt(900, 600)
	show := func(g *layout.Context) { sc.layoutTeamBoard(g, h.env, key) }
	h.frame(t, size, show)
	if sc.teamPending[read] {
		t.Fatal("a failed read must not re-queue by itself")
	}
	at := f32.Pt(130, 10)
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: at, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
	h.frame(t, size, show)
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: at, Source: pointer.Mouse})
	h.frame(t, size, show)
	if sc.teamFailedReads[read] || !sc.teamPending[read] {
		t.Fatalf("retry: failed=%v pending=%v", sc.teamFailedReads[read], sc.teamPending[read])
	}
}

func TestTeamListPrunesReadMarkers(t *testing.T) {
	t.Parallel()
	sc := teamShotScreens(t, tabChannel)
	sc.grid.doc.TeamRead = map[string]map[string]int64{keptTeam: {"a": 1}, "gone": {"a": 2}}
	before := sc.grid.doc.TeamRead
	sc.teamRead[keptTeam], sc.teamRead["gone"] = map[string]int64{"a": 1}, map[string]int64{"a": 2}
	sc.grid.dirty = false
	sc.applyTeamResult(&team.Result{Op: team.List, Epoch: sc.teamEpoch, Summaries: []client.TeamSummary{{TeamID: keptTeam}}})
	if _, ok := sc.grid.doc.TeamRead["gone"]; ok || sc.grid.doc.TeamRead[keptTeam]["a"] != 1 || !sc.grid.dirty {
		t.Fatalf("layout markers after prune = %v, dirty=%v", sc.grid.doc.TeamRead, sc.grid.dirty)
	}
	if _, ok := before["gone"]; !ok {
		t.Fatal("prune mutated the shared document map in place")
	}
	if _, ok := sc.teamRead["gone"]; ok {
		t.Fatal("runtime read marker kept")
	}
	sc.grid.dirty = false
	sc.applyTeamResult(&team.Result{Op: team.List, Epoch: sc.teamEpoch, Summaries: []client.TeamSummary{{TeamID: keptTeam}}})
	if sc.grid.dirty {
		t.Fatal("a prune with nothing to remove marked the grid dirty")
	}
}
