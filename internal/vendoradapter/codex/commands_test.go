package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

func TestCodexSlashTableRunsTheMeasuredRPCs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	if diff := cmp.Diff([]string{SlashCompact, SlashReview}, SlashCommandNames()); diff != "" {
		t.Fatalf("slash command names mismatch (-want +got):\n%s", diff)
	}

	rpcLog := filepath.Join(t.TempDir(), "rpcs.jsonl")
	adapter := NewAdapter(nil, WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeRecordRPCsConfigDir + ":" + rpcLog},
		SessionID: "session-slash-rpcs",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer closeTestSession(t, sess)

	codexSess, ok := sess.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", sess)
	}
	inputs := []struct {
		prompt     string
		wantMethod string
		wantSubstr string
	}{
		{prompt: SlashCompact, wantMethod: MethodThreadCompactStart, wantSubstr: `"threadId":"019a8000-0000-7000-8000-000000000001"`},
		{prompt: SlashReview, wantMethod: MethodReviewStart, wantSubstr: `"type":"uncommittedChanges"`},
		{prompt: "/review base main", wantMethod: MethodReviewStart, wantSubstr: `"type":"baseBranch","branch":"main"`},
		{prompt: "/review commit 1a2b3c4", wantMethod: MethodReviewStart, wantSubstr: `"type":"commit","sha":"1a2b3c4"`},
		{prompt: "/review focus on error handling", wantMethod: MethodReviewStart, wantSubstr: `"type":"custom","instructions":"focus on error handling"`},
	}

	for _, tc := range inputs {
		if _, sErr := sess.Send(ctx, vendor.Input{Text: tc.prompt}); sErr != nil {
			t.Fatalf("Send(%q) failed: %v", tc.prompt, sErr)
		}
		drainToTurnEnd(t, codexSess.Observations())
	}

	raw, err := os.ReadFile(filepath.Clean(rpcLog))
	if err != nil {
		t.Fatalf("read rpc log: %v", err)
	}
	logged := string(raw)
	for _, tc := range inputs {
		if !strings.Contains(logged, `"method":"`+tc.wantMethod+`"`) || !strings.Contains(logged, tc.wantSubstr) {
			t.Fatalf("for %q, expected method %s and %s in RPC log:\n%s", tc.prompt, tc.wantMethod, tc.wantSubstr, logged)
		}
	}

	// Invalid slash command arguments fail typed before reaching Codex.
	for _, bad := range []string{"/compact extra", "/review base", "/review base a b", "/review commit not-a-sha"} {
		if _, sErr := sess.Send(ctx, vendor.Input{Text: bad}); errcode.CodeOf(sErr) != errcode.InvalidArgument {
			t.Fatalf("Send(%q) err = %v, want invalid_argument", bad, sErr)
		}
	}
}

func TestFugueNamespaceNeverReachesCodex(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	rpcLog := filepath.Join(t.TempDir(), "rpcs.jsonl")
	adapter := NewAdapter(nil, WithBinary(fakeCodexBinary(t)))
	sess, err := adapter.Start(ctx, &vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      vendor.AccountHome{ConfigDir: fakeRecordRPCsConfigDir + ":" + rpcLog},
		SessionID: "session-fugue-namespace",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer closeTestSession(t, sess)

	for _, cmd := range []string{"/fugue:status", "/fugue:approve", "  /fugue:mode autonomous  ", "\n\t/fugue:lock"} {
		if _, sErr := sess.Send(ctx, vendor.Input{Text: cmd}); !errors.Is(sErr, vendor.ErrFugueCommand) {
			t.Fatalf("Send(%q) err = %v, want ErrFugueCommand", cmd, sErr)
		}
	}

	raw, err := os.ReadFile(filepath.Clean(rpcLog))
	if err != nil {
		t.Fatalf("read rpc log: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(strings.ToLower(line), "fugue:") {
			t.Fatalf("/fugue: command leaked to Codex app-server: %s", line)
		}
		var msg wireMessage
		if json.Unmarshal([]byte(line), &msg) == nil && msg.Method == MethodTurnStart {
			t.Fatalf("unexpected turn/start sent for /fugue: input: %s", line)
		}
	}
}

func drainToTurnEnd(t *testing.T, obsCh <-chan Observation) {
	t.Helper()
	for obs := range obsCh {
		if obs.TurnEnd != nil {
			return
		}
	}
	t.Fatal("observations channel closed before TurnEnd")
}

func closeTestSession(t *testing.T, sess vendor.Session) {
	t.Helper()
	if _, err := sess.Close(t.Context(), vendor.CloseUserRequest); err != nil {
		t.Errorf("Close session: %v", err)
	}
}
