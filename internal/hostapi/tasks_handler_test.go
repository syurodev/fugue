package hostapi

// F11.10b transport evidence (host-api §5.3, §7): a session-principal
// connection — a real vendor session's token attached — is denied typed for
// the tasks RPCs. The paging, posting, dedup and locked-vault behaviour of
// HA-E08 runs in internal/app (TestChannelHostAPIPagesPostsAndLocksMetadataOnly)
// with the same service the handler maps; the pkg/client test covers the wire
// encoding.

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

func TestChannelHostAPIDeniesSessionPrincipal(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	v.readySession(t, "cmd-lead", "", t.TempDir())

	// Attach the session token on a fresh connection: the tasks service must
	// refuse the session principal whatever the arguments say.
	_, attachHTTP, attachErr := attachSession(t, v.sockPath, sessionTokens(t, 1)[0])
	if attachErr != nil {
		t.Fatal(attachErr)
	}
	sessionTasks := tasksv1connect.NewTaskServiceClient(attachHTTP, "http://fugue")
	_, deniedErr := sessionTasks.PageChannel(t.Context(), connect.NewRequest(&tasksv1.PageChannelRequest{
		TeamId: "chan-e2e",
	}))
	if !errors.Is(deniedErr, ErrSessionPrincipalDenied) && connect.CodeOf(deniedErr) != connect.CodePermissionDenied {
		t.Fatalf("session principal PageChannel err = %v, want permission_denied", deniedErr)
	}
	_, deniedPost := sessionTasks.PostChannelMessage(t.Context(), connect.NewRequest(&tasksv1.PostChannelMessageRequest{
		CommandId: "post-1", TeamId: "chan-e2e", Text: "nope",
	}))
	if !errors.Is(deniedPost, ErrSessionPrincipalDenied) && connect.CodeOf(deniedPost) != connect.CodePermissionDenied {
		t.Fatalf("session principal PostChannelMessage err = %v, want permission_denied", deniedPost)
	}
	_, deniedPrune := sessionTasks.PruneWorktrees(t.Context(), connect.NewRequest(&tasksv1.PruneWorktreesRequest{}))
	if !errors.Is(deniedPrune, ErrSessionPrincipalDenied) && connect.CodeOf(deniedPrune) != connect.CodePermissionDenied {
		t.Fatalf("session principal PruneWorktrees err = %v, want permission_denied", deniedPrune)
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
	_, pageErr := localTasks.PageChannel(t.Context(), connect.NewRequest(&tasksv1.PageChannelRequest{TeamId: "missing-team"}))
	if connect.CodeOf(pageErr) != connect.CodeNotFound {
		t.Fatalf("local PageChannel err = %v, want not_found", pageErr)
	}
	_, postErr := localTasks.PostChannelMessage(t.Context(), connect.NewRequest(&tasksv1.PostChannelMessageRequest{TeamId: "missing-team", Text: "hi"}))
	if connect.CodeOf(postErr) != connect.CodeInvalidArgument && errcode.CodeOf(postErr) != errcode.InvalidArgument {
		t.Fatalf("local PostChannelMessage without command_id err = %v, want invalid_argument", postErr)
	}
	_, pruneErr := localTasks.PruneWorktrees(t.Context(), connect.NewRequest(&tasksv1.PruneWorktreesRequest{RetentionSeconds: -1}))
	if connect.CodeOf(pruneErr) != connect.CodeInvalidArgument && errcode.CodeOf(pruneErr) != errcode.InvalidArgument {
		t.Fatalf("local PruneWorktrees with negative retention err = %v, want invalid_argument", pruneErr)
	}
}
