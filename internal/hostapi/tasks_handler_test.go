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
}
