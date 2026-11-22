package hostapi

// F11.10b & F11.7a transport evidence (host-api §5.3, §7): a session-principal
// connection — a real vendor session's token attached — is denied typed for
// the tasks RPCs. The paging, posting, team read model, dedup, recovery and
// locked-vault behaviour runs in internal/app with the same service the handler
// maps; pkg/client tests cover the wire encoding.

import (
	"errors"
	"math"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

const (
	testTasksTeamID        = "chan-e2e"
	testTasksMissingTeamID = "missing-team"
)

func TestChannelHostAPIDeniesSessionPrincipal(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	v.readySession(t, "cmd-lead", "", t.TempDir())

	_, attachHTTP, attachErr := attachSession(t, v.sockPath, sessionTokens(t, 1)[0])
	if attachErr != nil {
		t.Fatal(attachErr)
	}
	sessionTasks := tasksv1connect.NewTaskServiceClient(attachHTTP, "http://fugue")
	assertSessionDeniedChannelRPCs(t, sessionTasks)
	assertSessionDeniedTeamRPCs(t, sessionTasks)
}

func assertSessionDeniedChannelRPCs(t *testing.T, sessionTasks tasksv1connect.TaskServiceClient) {
	t.Helper()
	_, deniedErr := sessionTasks.PageChannel(t.Context(), connect.NewRequest(&tasksv1.PageChannelRequest{
		TeamId: testTasksTeamID,
	}))
	if !errors.Is(deniedErr, ErrSessionPrincipalDenied) && connect.CodeOf(deniedErr) != connect.CodePermissionDenied {
		t.Fatalf("session principal PageChannel err = %v, want permission_denied", deniedErr)
	}
	_, deniedPost := sessionTasks.PostChannelMessage(t.Context(), connect.NewRequest(&tasksv1.PostChannelMessageRequest{
		CommandId: "post-1", TeamId: testTasksTeamID, Text: "nope",
	}))
	if !errors.Is(deniedPost, ErrSessionPrincipalDenied) && connect.CodeOf(deniedPost) != connect.CodePermissionDenied {
		t.Fatalf("session principal PostChannelMessage err = %v, want permission_denied", deniedPost)
	}
	_, deniedPrune := sessionTasks.PruneWorktrees(t.Context(), connect.NewRequest(&tasksv1.PruneWorktreesRequest{}))
	if !errors.Is(deniedPrune, ErrSessionPrincipalDenied) && connect.CodeOf(deniedPrune) != connect.CodePermissionDenied {
		t.Fatalf("session principal PruneWorktrees err = %v, want permission_denied", deniedPrune)
	}
}

func assertSessionDeniedTeamRPCs(t *testing.T, sessionTasks tasksv1connect.TaskServiceClient) {
	t.Helper()
	_, deniedList := sessionTasks.ListTeams(t.Context(), connect.NewRequest(&tasksv1.ListTeamsRequest{}))
	if !errors.Is(deniedList, ErrSessionPrincipalDenied) && connect.CodeOf(deniedList) != connect.CodePermissionDenied {
		t.Fatalf("session principal ListTeams err = %v, want permission_denied", deniedList)
	}
	_, deniedGet := sessionTasks.GetTeam(t.Context(), connect.NewRequest(&tasksv1.GetTeamRequest{TeamId: testTasksTeamID}))
	if !errors.Is(deniedGet, ErrSessionPrincipalDenied) && connect.CodeOf(deniedGet) != connect.CodePermissionDenied {
		t.Fatalf("session principal GetTeam err = %v, want permission_denied", deniedGet)
	}
	_, deniedRecover := sessionTasks.RecoverTask(t.Context(), connect.NewRequest(&tasksv1.RecoverTaskRequest{
		CommandId: "rec-1", TeamId: testTasksTeamID, TaskId: "t-1", Decision: "cancel",
	}))
	if !errors.Is(deniedRecover, ErrSessionPrincipalDenied) && connect.CodeOf(deniedRecover) != connect.CodePermissionDenied {
		t.Fatalf("session principal RecoverTask err = %v, want permission_denied", deniedRecover)
	}
}

func TestChannelHostAPIMapsLocalErrors(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	host, localHTTP := newTestClient(t, v.sockPath)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	localTasks := tasksv1connect.NewTaskServiceClient(localHTTP, "http://fugue")
	assertLocalChannelErrors(t, localTasks)
	assertLocalTeamErrors(t, localTasks)
}

func assertLocalChannelErrors(t *testing.T, localTasks tasksv1connect.TaskServiceClient) {
	t.Helper()
	_, pageErr := localTasks.PageChannel(t.Context(), connect.NewRequest(&tasksv1.PageChannelRequest{TeamId: testTasksMissingTeamID}))
	if connect.CodeOf(pageErr) != connect.CodeNotFound {
		t.Fatalf("local PageChannel err = %v, want not_found", pageErr)
	}
	_, postErr := localTasks.PostChannelMessage(t.Context(), connect.NewRequest(&tasksv1.PostChannelMessageRequest{TeamId: testTasksMissingTeamID, Text: "hi"}))
	if connect.CodeOf(postErr) != connect.CodeInvalidArgument && errcode.CodeOf(postErr) != errcode.InvalidArgument {
		t.Fatalf("local PostChannelMessage without command_id err = %v, want invalid_argument", postErr)
	}
	_, pruneErr := localTasks.PruneWorktrees(t.Context(), connect.NewRequest(&tasksv1.PruneWorktreesRequest{RetentionSeconds: -1}))
	if connect.CodeOf(pruneErr) != connect.CodeInvalidArgument && errcode.CodeOf(pruneErr) != errcode.InvalidArgument {
		t.Fatalf("local PruneWorktrees with negative retention err = %v, want invalid_argument", pruneErr)
	}
	_, overflowErr := localTasks.PruneWorktrees(t.Context(), connect.NewRequest(&tasksv1.PruneWorktreesRequest{RetentionSeconds: math.MaxInt64}))
	if connect.CodeOf(overflowErr) != connect.CodeInvalidArgument && errcode.CodeOf(overflowErr) != errcode.InvalidArgument {
		t.Fatalf("local PruneWorktrees with overflow retention err = %v, want invalid_argument", overflowErr)
	}
}

func assertLocalTeamErrors(t *testing.T, localTasks tasksv1connect.TaskServiceClient) {
	t.Helper()
	listRes, listErr := localTasks.ListTeams(t.Context(), connect.NewRequest(&tasksv1.ListTeamsRequest{}))
	if listErr != nil || len(listRes.Msg.GetTeams()) != 0 {
		t.Fatalf("local ListTeams = (%+v, %v), want empty slice and nil err", listRes, listErr)
	}
	_, getErr := localTasks.GetTeam(t.Context(), connect.NewRequest(&tasksv1.GetTeamRequest{TeamId: testTasksMissingTeamID}))
	if connect.CodeOf(getErr) != connect.CodeNotFound {
		t.Fatalf("local GetTeam err = %v, want not_found", getErr)
	}
	_, recErr := localTasks.RecoverTask(t.Context(), connect.NewRequest(&tasksv1.RecoverTaskRequest{
		CommandId: "rec-1", TeamId: testTasksMissingTeamID, TaskId: "t-1", Decision: "cancel",
	}))
	if connect.CodeOf(recErr) != connect.CodeNotFound {
		t.Fatalf("local RecoverTask on missing team err = %v, want not_found", recErr)
	}
}
