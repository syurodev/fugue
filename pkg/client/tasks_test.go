package client_test

// The tasks client over a real socket (F11.10b, HA §5.3): PageChannel and
// PostChannelMessage round-trip through fugue.tasks.v1, a failed post carries
// the public code, and the topic decoders separate content from the
// metadata-only shape a locked vault serves.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	hostv1connect "github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

const (
	testChannelTeamID = "chan-1"
	testChannelHello  = "channel hello"
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

// errTasksScripted is the static cause behind a scripted fake failure; it is
// wrapped, never constructed, at the call site.
var errTasksScripted = errors.New("scripted tasks error")

// errTasksDetail builds the coded failure of one mapped Connect code with
// the typed public-code detail, the way the host's handler maps its codes.
func errTasksDetail(code string, transport connect.Code) error {
	cerr := connect.NewError(transport, errTasksScripted)
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
	return serveFakeOnUnixSocket(t, mux)
}

func TestTasksClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fake := &tasksFake{posted: map[string]int64{}}
	c := serveTasksFake(t, fake)

	assertEmptyChannelPage(ctx, t, c)
	seq := assertTaskPost(ctx, t, c, &client.PostChannelMessageInput{
		CommandID: "post-1", TeamID: testChannelTeamID, ToTaskID: "task-a", Text: "hello from the app",
	}, 1)
	assertTaskPost(ctx, t, c, &client.PostChannelMessageInput{
		CommandID: "post-1", TeamID: testChannelTeamID, ToTaskID: "task-a", Text: "hello from the app",
	}, seq)
	assertTaskPagedLine(ctx, t, c, "hello from the app", "task-a")
	assertTaskCodedFailures(ctx, t, c)
}

// assertEmptyChannelPage pages a team with no lines.
func assertEmptyChannelPage(ctx context.Context, t *testing.T, c *client.Client) {
	t.Helper()
	lines, err := c.PageChannel(ctx, &client.PageChannelInput{TeamID: testChannelTeamID})
	if err != nil {
		t.Fatalf("PageChannel: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("PageChannel on an empty channel = %+v", lines)
	}
}

// assertTaskPost posts one message and expects the given `#n`.
func assertTaskPost(ctx context.Context, t *testing.T, c *client.Client, in *client.PostChannelMessageInput, want int64) int64 {
	t.Helper()
	seq, err := c.PostChannelMessage(ctx, in)
	if err != nil || seq != want {
		t.Fatalf("PostChannelMessage %s = (%d, %v), want (%d, nil)", in.CommandID, seq, err, want)
	}
	return seq
}

// assertTaskPagedLine pages the channel and expects one round-tripped line.
func assertTaskPagedLine(ctx context.Context, t *testing.T, c *client.Client, text, toTask string) {
	t.Helper()
	lines, err := c.PageChannel(ctx, &client.PageChannelInput{TeamID: testChannelTeamID})
	if err != nil || len(lines) != 1 {
		t.Fatalf("PageChannel = (%+v, %v), want the one posted line", lines, err)
	}
	if lines[0].ChannelSeq != 1 || lines[0].SenderKind != "user" || lines[0].RecipientTaskID != toTask || lines[0].Content != text {
		t.Fatalf("posted line = %+v, want the round-tripped content", lines[0])
	}
}

// assertTaskCodedFailures checks the typed codes of the failing calls.
func assertTaskCodedFailures(ctx context.Context, t *testing.T, c *client.Client) {
	t.Helper()
	for _, tc := range []struct {
		input *client.PostChannelMessageInput
		code  string
	}{
		{&client.PostChannelMessageInput{CommandID: "post-ended", TeamID: "ended", Text: "too late"}, "tasks.team_ended"},
		{&client.PostChannelMessageInput{CommandID: "post-too-large", TeamID: testChannelTeamID, Text: "x"}, "tasks.post_too_large"},
	} {
		if _, err := c.PostChannelMessage(ctx, tc.input); err == nil {
			t.Fatalf("post %s succeeded, want a coded failure", tc.input.CommandID)
		} else if code, ok := client.TasksCodeOf(err); !ok || code != tc.code {
			t.Fatalf("post %s code = (%q, %v), want %q", tc.input.CommandID, code, ok, tc.code)
		}
	}
	if _, err := c.PageChannel(ctx, &client.PageChannelInput{TeamID: "missing"}); err == nil {
		t.Fatal("PageChannel on a missing team succeeded")
	} else if code, ok := client.TasksCodeOf(err); !ok || code != "tasks.team_not_found" {
		t.Fatalf("missing team code = (%q, %v), want tasks.team_not_found", code, ok)
	}
}

func TestTeamChannelTopicDecoders(t *testing.T) {
	t.Parallel()
	assertUnlockedDecode(t)
	assertLockedDecode(t)
	assertDecodeRejections(t)
}

// assertUnlockedDecode checks the unlocked snapshot and event decode with
// their content.
func assertUnlockedDecode(t *testing.T) {
	t.Helper()
	taskUnlockedSnapshot := []byte(`{"lines":[{"channel_seq":3,"kind":"send","sender_kind":"user","recipient_task_id":"task-a","thread_root_task_id":"","source_seq":9,"created_at":1700000000000,"content":"` + testChannelHello + `"}]}`)
	taskUnlockedLine := []byte(`{"channel_seq":3,"kind":"send","sender_kind":"user","recipient_task_id":"task-a","thread_root_task_id":"","source_seq":9,"created_at":1700000000000,"content":"` + testChannelHello + `"}`)
	snap, err := client.DecodeTeamChannelSnapshot(taskUnlockedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Sequence != 3 || len(snap.Lines) != 1 || snap.Lines[0].Content != testChannelHello {
		t.Fatalf("snapshot = %+v, want the line with its content at sequence 3", snap)
	}
	ev, err := client.DecodeTeamChannelEvent(client.EventTeamChannelAppended, taskUnlockedLine)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Locked || ev.Line.Content != testChannelHello || ev.Line.CreatedAt.IsZero() {
		t.Fatalf("event = %+v, want the unlocked line", ev)
	}
}

// assertLockedDecode checks the metadata-only shapes.
func assertLockedDecode(t *testing.T) {
	t.Helper()
	taskLockedSnapshot := []byte(`{"lines":[{"channel_seq":3,"kind":"stall","sender_kind":"watchdog","thread_root_task_id":"task-a","source_seq":9,"created_at":1700000000000}]}`)
	taskLockedLine := []byte(`{"channel_seq":3,"kind":"stall","sender_kind":"watchdog","thread_root_task_id":"task-a","source_seq":9,"created_at":1700000000000}`)
	lockedSnap, err := client.DecodeTeamChannelSnapshot(taskLockedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if lockedSnap.Lines[0].Content != "" {
		t.Fatalf("locked snapshot line = %+v, want metadata only", lockedSnap.Lines[0])
	}
	lockedEvent, err := client.DecodeTeamChannelEvent(client.EventTeamChannelAppended, taskLockedLine)
	if err != nil {
		t.Fatal(err)
	}
	if !lockedEvent.Locked || lockedEvent.Line.Content != "" || lockedEvent.Line.Kind != "stall" {
		t.Fatalf("locked event = %+v, want metadata only", lockedEvent)
	}
}

// assertDecodeRejections checks the unknown event types, negative sequences,
// and non-object payloads fail typed.
func assertDecodeRejections(t *testing.T) {
	t.Helper()
	taskUnlockedSnapshot := []byte(`{"lines":[{"channel_seq":3,"kind":"send","sender_kind":"user","recipient_task_id":"task-a","thread_root_task_id":"","source_seq":9,"created_at":1700000000000,"content":"` + testChannelHello + `"}]}`)
	if _, err := client.DecodeTeamChannelEvent("something.else", taskUnlockedSnapshot); err == nil {
		t.Fatal("an unknown event type decoded")
	}
	if _, err := client.DecodeTeamChannelEvent(client.EventTeamChannelAppended, []byte("null")); err == nil {
		t.Fatal("a null payload decoded")
	}
	if _, err := client.DecodeTeamChannelSnapshot([]byte(`{"lines":[{"channel_seq":-1,"kind":"send","sender_kind":"user","source_seq":1,"created_at":1700000000000}]}`)); err == nil {
		t.Fatal("a negative channel_seq snapshot decoded")
	}
}
