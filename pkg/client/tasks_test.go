package client_test

// The tasks client over a real socket (F11.10b, HA §5.3): PageChannel and
// PostChannelMessage round-trip through fugue.tasks.v1, a failed post carries
// the public code, and the topic decoders separate content from the
// metadata-only shape a locked vault serves.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	hostv1connect "github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

// tasksFake serves the tasks service with scripted lines and one failing
// post, the way the appstate fake serves its service.
type tasksFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	tasksv1connect.UnimplementedTaskServiceHandler

	mu     sync.Mutex
	posted map[string]int64
	lines  []*tasksv1.ChannelLine
}

func (f *tasksFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{ApiVersion: "v1"}), nil
}

func (f *tasksFake) PageChannel(_ context.Context, req *connect.Request[tasksv1.PageChannelRequest]) (*connect.Response[tasksv1.PageChannelResponse], error) {
	if req.Msg.GetTeamId() == "missing" {
		return nil, errTasksDetail("tasks.team_not_found", connect.CodeNotFound)
	}
	out := make([]*tasksv1.ChannelLine, 0, len(f.lines))
	for i := len(f.lines) - 1; i >= 0; i-- {
		if req.Msg.GetThreadTaskId() == "" || f.lines[i].GetThreadRootTaskId() == req.Msg.GetThreadTaskId() {
			out = append(out, f.lines[i])
		}
	}
	return connect.NewResponse(&tasksv1.PageChannelResponse{Lines: out}), nil
}

func (f *tasksFake) PostChannelMessage(_ context.Context, req *connect.Request[tasksv1.PostChannelMessageRequest]) (*connect.Response[tasksv1.PostChannelMessageResponse], error) {
	if req.Msg.GetTeamId() == "ended" {
		return nil, errTasksDetail("tasks.team_ended", connect.CodeFailedPrecondition)
	}
	if req.Msg.GetCommandId() == "post-too-large" {
		return nil, errTasksDetail("tasks.post_too_large", connect.CodeResourceExhausted)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if seq, ok := f.posted[req.Msg.GetCommandId()]; ok {
		return connect.NewResponse(&tasksv1.PostChannelMessageResponse{ChannelSeq: seq}), nil
	}
	seq := int64(len(f.lines)) + 1
	f.posted[req.Msg.GetCommandId()] = seq
	f.lines = append(f.lines, &tasksv1.ChannelLine{
		ChannelSeq: seq, Kind: "send", SenderKind: "user",
		RecipientTaskId: req.Msg.GetToTaskId(), ThreadRootTaskId: req.Msg.GetThreadTaskId(),
		Attempt: 1, Content: req.Msg.GetText(),
	})
	return connect.NewResponse(&tasksv1.PostChannelMessageResponse{ChannelSeq: seq}), nil
}

// errTasksDetail builds the coded failure of one mapped Connect code with
// the typed public-code detail, the way the host's handler maps its codes.
func errTasksDetail(code string, transport connect.Code) error {
	cerr := connect.NewError(transport, errors.New("scripted"))
	if detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code}); derr == nil {
		cerr.AddDetail(detail)
	}
	return cerr
}

func serveTasksFake(t *testing.T, fake *tasksFake) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(tasksv1connect.NewTaskServiceHandler(fake))
	socket := filepath.Join(shortDir(t), "tasks.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatalf("dial the scripted host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestTasksClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fake := &tasksFake{posted: map[string]int64{}}
	c := serveTasksFake(t, fake)

	// An empty channel pages nothing.
	lines, err := c.PageChannel(ctx, &client.PageChannelInput{TeamID: "chan-1"})
	if err != nil {
		t.Fatalf("PageChannel: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("PageChannel on an empty channel = %+v", lines)
	}

	// A post returns the new `#n`, and the line round-trips through the page.
	seq, err := c.PostChannelMessage(ctx, &client.PostChannelMessageInput{
		CommandID: "post-1", TeamID: "chan-1", ToTaskID: "task-a", Text: "hello from the app",
	})
	if err != nil || seq != 1 {
		t.Fatalf("PostChannelMessage = (%d, %v), want (1, nil)", seq, err)
	}
	if _, err := c.PostChannelMessage(ctx, &client.PostChannelMessageInput{
		CommandID: "post-1", TeamID: "chan-1", ToTaskID: "task-a", Text: "hello from the app",
	}); err != nil {
		t.Fatalf("replayed PostChannelMessage: %v", err)
	}
	lines, err = c.PageChannel(ctx, &client.PageChannelInput{TeamID: "chan-1"})
	if err != nil || len(lines) != 1 {
		t.Fatalf("PageChannel = (%+v, %v), want the one posted line", lines, err)
	}
	if lines[0].ChannelSeq != 1 || lines[0].SenderKind != "user" || lines[0].RecipientTaskID != "task-a" || lines[0].Content != "hello from the app" {
		t.Fatalf("posted line = %+v, want the round-tripped content", lines[0])
	}

	// The typed codes surface through the detail.
	if _, err := c.PageChannel(ctx, &client.PageChannelInput{TeamID: "missing"}); err == nil {
		t.Fatal("PageChannel on a missing team succeeded")
	} else if code, ok := client.TasksCodeOf(err); !ok || code != "tasks.team_not_found" {
		t.Fatalf("missing team code = (%q, %v), want tasks.team_not_found", code, ok)
	}
	if _, err := c.PostChannelMessage(ctx, &client.PostChannelMessageInput{
		CommandID: "post-ended", TeamID: "ended", Text: "too late",
	}); err == nil {
		t.Fatal("a post to an ended team succeeded")
	} else if code, ok := client.TasksCodeOf(err); !ok || code != "tasks.team_ended" {
		t.Fatalf("ended team code = (%q, %v), want tasks.team_ended", code, ok)
	}
	if _, err := c.PostChannelMessage(ctx, &client.PostChannelMessageInput{
		CommandID: "post-too-large", TeamID: "chan-1", Text: "x",
	}); err == nil {
		t.Fatal("an oversized post succeeded")
	} else if code, ok := client.TasksCodeOf(err); !ok || code != "tasks.post_too_large" {
		t.Fatalf("oversized post code = (%q, %v), want tasks.post_too_large", code, ok)
	}
}

func TestTeamChannelTopicDecoders(t *testing.T) {
	t.Parallel()

	// The unlocked shape: metadata plus content.
	unlocked := []byte(`{"lines":[{"channel_seq":3,"kind":"send","sender_kind":"user","recipient_task_id":"task-a","thread_root_task_id":"","source_seq":9,"created_at":1700000000000,"content":"hello"}]}`)
	snap, err := client.DecodeTeamChannelSnapshot(unlocked)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Sequence != 3 || len(snap.Lines) != 1 || snap.Lines[0].Content != "hello" {
		t.Fatalf("snapshot = %+v, want the line with its content at sequence 3", snap)
	}
	lineWire := []byte(`{"channel_seq":3,"kind":"send","sender_kind":"user","recipient_task_id":"task-a","thread_root_task_id":"","source_seq":9,"created_at":1700000000000,"content":"hello"}`)
	ev, err := client.DecodeTeamChannelEvent(client.EventTeamChannelAppended, lineWire)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Locked || ev.Line.Content != "hello" || ev.Line.CreatedAt.IsZero() {
		t.Fatalf("event = %+v, want the unlocked line", ev)
	}

	// The locked shape: metadata only, no content anywhere.
	locked := []byte(`{"lines":[{"channel_seq":3,"kind":"stall","sender_kind":"watchdog","thread_root_task_id":"task-a","source_seq":9,"created_at":1700000000000}]}`)
	lockedSnap, err := client.DecodeTeamChannelSnapshot(locked)
	if err != nil {
		t.Fatal(err)
	}
	if lockedSnap.Lines[0].Content != "" {
		t.Fatalf("locked snapshot line = %+v, want metadata only", lockedSnap.Lines[0])
	}
	lockedWire := []byte(`{"channel_seq":3,"kind":"stall","sender_kind":"watchdog","thread_root_task_id":"task-a","source_seq":9,"created_at":1700000000000}`)
	lockedEvent, err := client.DecodeTeamChannelEvent(client.EventTeamChannelAppended, lockedWire)
	if err != nil {
		t.Fatal(err)
	}
	if !lockedEvent.Locked || lockedEvent.Line.Content != "" || lockedEvent.Line.Kind != "stall" {
		t.Fatalf("locked event = %+v, want metadata only", lockedEvent)
	}

	// Unknown event types and non-object payloads fail typed.
	if _, err := client.DecodeTeamChannelEvent("something.else", unlocked); err == nil {
		t.Fatal("an unknown event type decoded")
	}
	if _, err := client.DecodeTeamChannelEvent(client.EventTeamChannelAppended, []byte("null")); err == nil {
		t.Fatal("a null payload decoded")
	}
}
