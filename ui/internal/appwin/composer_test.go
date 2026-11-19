package appwin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The strings the tests assert on, which goconst counts across the package.
const (
	callSend      = "send"
	callSteer     = "steer"
	callStart     = "start"
	startedID     = "started-1"
	testWorkspace = "ws1"
)

// vendorCall is one state-changing vendor RPC the fake host recorded.
type vendorCall struct {
	method, commandID, session, text, workspace, root, model, effort string
}

// fakeVendorHost records the composer's writes and answers them; the composer
// tests pin what each action posts without a real vendor CLI.
type fakeVendorHost struct {
	hostv1connect.UnimplementedHostServiceHandler
	vendorsv1connect.UnimplementedVendorServiceHandler
	mu    sync.Mutex
	calls []vendorCall
}

func (f *fakeVendorHost) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{}), nil
}

func (f *fakeVendorHost) record(call vendorCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeVendorHost) Send(ctx context.Context, req *connect.Request[vendorsv1.SendRequest]) (*connect.Response[vendorsv1.SendResponse], error) {
	m := req.Msg
	f.record(vendorCall{method: callSend, commandID: m.CommandId, session: m.SessionId, text: m.Text})
	return connect.NewResponse(&vendorsv1.SendResponse{}), nil
}

func (f *fakeVendorHost) Steer(ctx context.Context, req *connect.Request[vendorsv1.SteerRequest]) (*connect.Response[vendorsv1.SteerResponse], error) {
	m := req.Msg
	f.record(vendorCall{method: callSteer, commandID: m.CommandId, session: m.SessionId, text: m.Text})
	return connect.NewResponse(&vendorsv1.SteerResponse{}), nil
}

func (f *fakeVendorHost) Interrupt(ctx context.Context, req *connect.Request[vendorsv1.InterruptRequest]) (*connect.Response[vendorsv1.InterruptResponse], error) {
	f.record(vendorCall{method: "interrupt", commandID: req.Msg.CommandId, session: req.Msg.SessionId})
	return connect.NewResponse(&vendorsv1.InterruptResponse{Status: vendorsv1.OperationStatus_OPERATION_STATUS_DONE}), nil
}

func (f *fakeVendorHost) StartSession(ctx context.Context, req *connect.Request[vendorsv1.StartSessionRequest]) (*connect.Response[vendorsv1.StartSessionResponse], error) {
	m := req.Msg
	f.record(vendorCall{
		method: callStart, commandID: m.CommandId, text: m.Vendor.String(),
		workspace: m.WorkspaceId, root: m.RootName,
		model: m.Profile.GetModel(), effort: m.Profile.GetEffort(),
	})
	return connect.NewResponse(&vendorsv1.StartSessionResponse{
		Session: &vendorsv1.VendorSession{SessionId: "started-1", Vendor: m.Vendor, State: "starting"},
	}), nil
}

// taken returns the calls the fake host has taken so far.
func (f *fakeVendorHost) taken() []vendorCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]vendorCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// composerSocketDir is the fake host's socket directory: short enough for the
// unix socket path, which t.TempDir() cannot promise on macOS — sun_path
// holds 104 bytes and macOS test temp dirs already exceed 100. The test name
// and process separate the parallel and concurrent runs.
func composerSocketDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fg-%x-%d", sum[:4], os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			return // nothing the test could do; the temp dir is disposable
		}
	})
	return dir
}

// serveFakeVendorHost serves the fake host on a unix socket over unencrypted
// HTTP/2 and dials a client at it.
func serveFakeVendorHost(t *testing.T) (*client.Client, *fakeVendorHost) {
	t.Helper()
	fake := &fakeVendorHost{}
	socket := filepath.Join(composerSocketDir(t), "host.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(vendorsv1connect.NewVendorServiceHandler(fake))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, client.Params{Kind: client.KindApp, Name: "test", Version: "0", APIVersions: []string{"v1"}})
	if err != nil {
		t.Fatalf("dial the fake host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, fake
}

// frameRunner is the composer tests' headless frame loop: layout, hand the
// ops to the router, queue events, lay out again, without a window.
type frameRunner struct {
	router *input.Router
	ops    op.Ops
	env    *kit.Env
	gtx    layout.Context
	now    time.Time
}

func newFrameRunner(t *testing.T) *frameRunner {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build the shaper: %v", err)
	}
	th := theme.Dark(sh)
	return &frameRunner{
		router: new(input.Router),
		env:    &kit.Env{Th: &th, Sh: sh},
		now:    time.Unix(1_700_000_000, 0),
	}
}

// frame lays one frame out at size and runs fn inside it.
func (h *frameRunner) frame(t *testing.T, size image.Point, fn func(gtx *layout.Context)) {
	t.Helper()
	h.ops.Reset()
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	h.gtx = layout.Context{
		Ops:         &h.ops,
		Metric:      h.env.Metric(device),
		Constraints: layout.Exact(size),
		Source:      h.router.Source(),
	}
	h.gtx.Now = h.now
	h.env.Begin(h.now)
	h.env.TrackInput(&h.gtx)
	fn(&h.gtx)
	h.env.FrameEnd(&h.gtx)
	h.router.Frame(&h.ops)
	h.now = h.now.Add(time.Second / 60)
}

// focus lays one composer frame out and focuses its input inside it, the way
// the user's click does: the editor's filter must exist before the events a
// later frame delivers.
func (h *frameRunner) focus(t *testing.T, fx *composerFixture) {
	t.Helper()
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
		gtx.Execute(key.FocusCmd{Tag: fx.state().area.Tag()})
	})
}

// typeInto delivers text the way a desktop window delivers typed text: an
// EditEvent over the caret's range — the caret the draft's length puts at the
// end — followed by the SelectionEvent that places the caret after the
// committed text, the commit-then-selection order the IME tests model.
func (h *frameRunner) typeInto(st *composerState, text string) {
	at := len([]rune(st.area.Text()))
	end := at + len([]rune(text))
	h.router.Queue(key.EditEvent{Text: text, Range: key.Range{Start: at, End: at}})
	h.router.Queue(key.SelectionEvent{Start: end, End: end})
}

// tapReturn queues one Return press and its release with the modifiers, the
// way a keystroke arrives; the composer's flows tap only Return.
func (h *frameRunner) tapReturn(mod key.Modifiers) {
	h.router.Queue(key.Event{Name: key.NameReturn, Modifiers: mod, State: key.Press})
	h.router.Queue(key.Event{Name: key.NameReturn, Modifiers: mod, State: key.Release})
}

// composerFixture is a screens wired to a fake host: one ready session open,
// its composer state built, and the poster running against the fake host.
type composerFixture struct {
	sc     *screens
	poster *session.Poster
	fake   *fakeVendorHost
	c      *composer
}

// newComposerFixture builds the fixture; vendor holds the probed
// capabilities the composer gates its actions on.
func newComposerFixture(t *testing.T, state string, capability client.Capability) *composerFixture {
	t.Helper()
	cOf, fake := serveFakeVendorHost(t)
	poster := session.NewPoster(func() *client.Client { return cOf })
	poster.Start(t.Context())
	t.Cleanup(poster.Stop)
	fake2 := &fakeFetcher{}
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, fake2, poster)
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "s1", Vendor: client.VendorClaude, State: state},
	}})
	sc.openSession("s1")
	sc.composer.applyProbes([]client.VendorProbe{{Vendor: client.VendorClaude, Capability: capability}})
	return &composerFixture{sc: sc, poster: poster, fake: fake, c: sc.composer}
}

// state is the fixture's composer state for the open session.
func (fx *composerFixture) state() *composerState { return fx.c.stateFor("s1") }

// awaitWrite waits for one post's result, which the poster delivers after the
// host took the call, and returns the calls the fake host recorded.
func (fx *composerFixture) awaitWrite() []vendorCall {
	res := <-fx.poster.Results()
	if res.Err != nil {
		return nil
	}
	return fx.fake.taken()
}

// TestComposerActionFollowsTheSessionState pins the gating: send on ready,
// steer on running or awaiting approval when the vendor steers, stop on an
// active turn, and nothing on the states that cannot accept input.
func TestComposerActionFollowsTheSessionState(t *testing.T) {
	t.Parallel()
	c := newComposer(nil)
	c.applyProbes([]client.VendorProbe{{Vendor: client.VendorClaude, Capability: client.Capability{Steer: true, Interrupt: true}}})
	c.applyProbes([]client.VendorProbe{{Vendor: "agy", Capability: client.Capability{Interrupt: true}}})
	for tc, want := range map[string]struct {
		kind session.PostKind
		ok   bool
		stop bool
	}{
		stateReady:            {session.PostSend, true, false},
		stateRunning:          {session.PostSteer, true, true},
		stateAwaitingApproval: {session.PostSteer, true, true},
		stateStarting:         {0, false, false},
		stateWaiting:          {0, false, false},
		stateDetached:         {0, false, false},
		stateClosed:           {0, false, false},
	} {
		kind, ok := c.action(session.Session{Vendor: client.VendorClaude, State: tc})
		if kind != want.kind || ok != want.ok || turnActive(tc) != want.stop {
			t.Fatalf("state %s action = %d, %v, stop %v; want %d, %v, %v", tc, kind, ok, turnActive(tc), want.kind, want.ok, want.stop)
		}
	}
	// A vendor without the steer capability takes no mid-turn input; its
	// running turn is still stoppable when it interrupts.
	if kind, ok := c.action(session.Session{Vendor: "agy", State: stateRunning}); ok || kind != 0 {
		t.Fatalf("agy running action = %d, %v; want none", kind, ok)
	}
	if !turnActive(stateRunning) {
		t.Fatal("agy's running turn is not stoppable")
	}
}

// TestComposerEnterSendsTheReadyDraft pins the ready flow: Enter submits the
// draft, the send posts with its own command id to the open session, and the
// draft clears once the post is queued.
func TestComposerEnterSendsTheReadyDraft(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{Steer: true, Interrupt: true})
	h := newFrameRunner(t)
	st := fx.state()
	h.focus(t, fx)
	h.typeInto(st, "hello")
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	if got := st.area.Text(); got != "hello" {
		t.Fatalf("draft = %q after typing, want hello", got)
	}

	h.tapReturn(0)
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	if got := st.area.Text(); got != "" {
		t.Fatalf("draft = %q after the send, want it cleared", got)
	}
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].method != callSend || calls[0].text != "hello" || calls[0].session != "s1" || calls[0].commandID == "" {
		t.Fatalf("calls = %+v, want one send of hello to s1 with a command id", calls)
	}
}

// TestComposerEnterSteersWhileTheTurnRuns pins the running flow: the primary
// action is steer and Enter posts the draft as one.
func TestComposerEnterSteersWhileTheTurnRuns(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateRunning, client.Capability{Steer: true, Interrupt: true})
	h := newFrameRunner(t)
	h.focus(t, fx)
	h.typeInto(fx.state(), "one more thing")
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})

	h.tapReturn(0)
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].method != callSteer || calls[0].text != "one more thing" {
		t.Fatalf("calls = %+v, want one steer of the draft", calls)
	}
}

// TestComposerShiftEnterKeepsTheDraft pins the newline rule: Shift+Return
// inserts the newline and never submits — the draft survives for the send.
func TestComposerShiftEnterKeepsTheDraft(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	h := newFrameRunner(t)
	st := fx.state()
	h.focus(t, fx)
	h.typeInto(fx.state(), "line one")
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})

	h.tapReturn(key.ModShift)
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	if got := st.area.Text(); got != "line one\n" {
		t.Fatalf("draft = %q after shift+return, want the newline and the draft intact", got)
	}
	if calls := fx.fake.taken(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none: the newline is not a submit", calls)
	}
}

// TestComposerSlashMenuChoosesTheWholePrompt pins the §9 menu: typing "/"
// suggests the commands, Return chooses and the command becomes the whole
// prompt, and the next Return sends it — the menu does not reopen on the
// command it just wrote.
func TestComposerSlashMenuChoosesTheWholePrompt(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	fx.sc.commandsFor("s1").ApplyList(client.Commands{Revision: 1, Groups: []client.CommandGroup{
		{Name: "vendor", Vendor: []string{"clear"}},
	}})
	h := newFrameRunner(t)
	st := fx.state()
	h.focus(t, fx)
	h.typeInto(st, "/")
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	if !st.menu.IsOpen() {
		t.Fatal("the slash menu did not open on the command draft")
	}

	// Return chooses; the command is the whole prompt and nothing posts.
	h.tapReturn(0)
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	if got := st.area.Text(); got != "/clear" {
		t.Fatalf("draft = %q after the choice, want the whole prompt /clear", got)
	}
	if st.menu.IsOpen() {
		t.Fatal("the slash menu stayed open on the command it wrote")
	}

	// The next Return sends the command as the whole prompt.
	h.tapReturn(0)
	h.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(h.env, gtx, fx.sc, true)
	})
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].method != callSend || calls[0].text != "/clear" {
		t.Fatalf("calls = %+v, want one send of /clear as the whole prompt", calls)
	}
}

// TestComposerRestoresAFailedSend pins the failure rule: a send the host
// refused puts the user's words back while the draft is empty, and the
// failure shows under the input.
func TestComposerRestoresAFailedSend(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	st := fx.state()
	fx.sc.applyPostResult(session.PostResult{
		Kind: session.PostSend, Session: "s1", Text: "the user's words", Err: client.ErrDisconnected,
	})
	if got := st.area.Text(); got != "the user's words" {
		t.Fatalf("draft = %q after the failed send, want the words back", got)
	}
	if st.lastErr == "" {
		t.Fatal("the failed send left no error under the input")
	}
}

// TestStartFlowPostsTheChosenSession pins the start dialog: the chosen
// workspace, root and profile reach the start command, a single-root
// workspace leaves the root name empty for the host to resolve, and the
// dialog's busy mark clears when the post answers.
func TestStartFlowPostsTheChosenSession(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	f := fx.c.start
	f.applyWorkspaces([]client.WorkspaceSummary{{ID: "ws1", Name: "backend"}})
	f.chooseWorkspace(testWorkspace, fx.sc)
	f.applyDetail(client.Workspace{ID: "ws1", Roots: []client.WorkspaceRoot{{Name: "main"}}})
	f.model.SetText("sonnet")
	f.effortSel = "high"

	f.submit(fx.c)
	if !f.busy {
		t.Fatal("the start post did not mark the dialog busy")
	}
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].method != callStart || calls[0].text != "VENDOR_CLAUDE" ||
		calls[0].workspace != testWorkspace || calls[0].root != "" || calls[0].model != "sonnet" || calls[0].effort != "high" {
		t.Fatalf("calls = %+v, want one start of claude in ws1 with the chosen profile", calls)
	}

	// The result clears the busy mark and the screens remember the session
	// to open on its created event.
	fx.sc.applyPostResult(session.PostResult{Kind: session.PostStart, Started: startedID})
	if f.busy {
		t.Fatal("the start result left the dialog busy")
	}
	if fx.sc.lastStarted != startedID {
		t.Fatalf("lastStarted = %q, want the session the start created", fx.sc.lastStarted)
	}
}

// TestStartFlowRefusesASubmitWithoutAWorkspace pins the form rule: a start
// without a chosen workspace does not post and names what is missing.
func TestStartFlowRefusesASubmitWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	f := fx.c.start
	f.model.SetText("sonnet")
	f.submit(fx.c)
	if f.busy {
		t.Fatal("a start without a workspace posted")
	}
	if f.lastErr == "" {
		t.Fatal("the refused start named nothing")
	}
	if calls := fx.fake.taken(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none", calls)
	}
}

// TestComposerLaysOutWithinItsRowHeight pins the row contract the transcript
// pane's split relies on: the composer laid out under the row's constraints
// fills exactly the height the pane measured for it, even when the content
// shrinks between the measure and the draw (here: the write-failure line
// clears). The call site passes the constrained context (govet's unusedwrite
// guard pins that); this pins what the constraints are for.
func TestComposerLaysOutWithinItsRowHeight(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	h := newFrameRunner(t)
	st := fx.state()
	const width = 600
	var rowH int
	h.frame(t, image.Pt(width, 400), func(gtx *layout.Context) {
		st.lastErr = "the host refused the send"
		rowH = fx.c.measure(h.env, gtx, fx.sc)
	})

	var row layout.Dimensions
	h.frame(t, image.Pt(width, 400), func(gtx *layout.Context) {
		st.lastErr = ""
		local := *gtx
		local.Constraints = layout.Constraints{
			Min: image.Pt(width, rowH), Max: image.Pt(width, rowH),
		}
		row = fx.c.layout(h.env, &local, fx.sc, true)
	})
	if row.Size.Y != rowH {
		t.Fatalf("composer row = %d px, want the row height %d the pane allotted", row.Size.Y, rowH)
	}
	if row.Size.Y > rowH {
		t.Fatalf("composer row = %d px overflows its row height %d", row.Size.Y, rowH)
	}
}
