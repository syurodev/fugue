package agy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	testSessionValue = "c2Vzc2lvbi10b2tlbi1mb3ItdGhlLWFyZ3YtdGVzdA"
	testFugueBinary  = "/opt/fugue/bin/fugue"
	testFugueSocket  = "/run/user/1000/fugue/fugue.sock"
)

// TestAgySessionNewCarriesTheFugueBridge (F9.11 evidence): session/new and
// session/load carry the fugue bridge as an mcpServers entry over the ACP
// pipe. The entry carries no env — the token reaches the bridge by
// inheritance from the agy process environment — so no token value is on the
// wire or in any file Fugue writes (vendor-runtime §12).
func TestAgySessionNewCarriesTheFugueBridge(t *testing.T) {
	t.Parallel()
	bridge := vendor.Bridge{Binary: testFugueBinary, Socket: testFugueSocket}

	newLog := newBridgedSessionRPCLog(t, "", bridge)
	var got []McpServer
	if findOpenMethod(t, newLog) != MethodSessionNew {
		t.Fatalf("expected session/new in %s", newLog)
	}
	got = openMcpServers(t, newLog)
	want := []McpServer{{Name: BridgeServerName, Command: testFugueBinary, Args: []string{"mcp-bridge", "--socket", testFugueSocket}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("session/new mcpServers (-want +got):\n%s", diff)
	}

	resumeLog := newBridgedSessionRPCLog(t, "2a8b7877-ac14-403d-a8f7-7063f8164df9", bridge)
	if findOpenMethod(t, resumeLog) != MethodSessionLoad {
		t.Fatalf("expected session/load in %s", resumeLog)
	}
	if diff := cmp.Diff(want, openMcpServers(t, resumeLog)); diff != "" {
		t.Errorf("session/load mcpServers (-want +got):\n%s", diff)
	}

	// No token value anywhere on the pipe: the only mention of the token is
	// its name in the environment of the process Fugue spawns, never in a
	// frame Fugue writes.
	for _, log := range []string{newLog, resumeLog} {
		if strings.Contains(string(readFile(t, log)), testSessionValue) {
			t.Errorf("the session token reached the ACP pipe: %s", log)
		}
	}

	// Without a bridge the list stays empty, as pre-F9.11 sessions sent.
	emptyLog := newBridgedSessionRPCLog(t, "", vendor.Bridge{})
	if empty := string(readFile(t, emptyLog)); strings.Contains(empty, `"mcpServers":[{`) || !strings.Contains(empty, `"mcpServers":[]`) {
		t.Errorf("session without a bridge sent %q", empty)
	}

	// Host paths only: a relative binary or socket is refused before spawn.
	adapter := newTestAdapter(t, clock.NewFake(testEpoch()))
	for _, relative := range []vendor.Bridge{{Binary: "fugue", Socket: testFugueSocket}, {Binary: testFugueBinary, Socket: "fugue.sock"}} {
		if _, err := adapter.Start(t.Context(), &vendor.StartSpec{Bridge: relative}); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Errorf("relative bridge path %+v = %v, want invalid_argument", relative, err)
		}
	}
}

// openRequest returns the method and params of the session/new or
// session/load request recorded in the RPC log.
type openRequest struct {
	Method string `json:"method"`
	Params struct {
		McpServers []McpServer `json:"mcpServers"`
	} `json:"params"`
}

func findOpenRPC(t *testing.T, log string) openRequest {
	t.Helper()
	for _, line := range nonEmptyLines(string(readFile(t, log))) {
		var req openRequest
		if json.Unmarshal([]byte(line), &req) != nil || req.Method == "" {
			continue
		}
		if req.Method == MethodSessionNew || req.Method == MethodSessionLoad {
			return req
		}
	}
	t.Fatalf("no session/new or session/load request in %s", log)
	return openRequest{}
}

func findOpenMethod(t *testing.T, log string) string {
	t.Helper()
	return findOpenRPC(t, log).Method
}

func openMcpServers(t *testing.T, log string) []McpServer {
	t.Helper()
	return findOpenRPC(t, log).Params.McpServers
}

// newBridgedSessionRPCLog starts a fake session whose RPCs are recorded and
// returns the log path. The resume id switches the handshake to session/load.
func newBridgedSessionRPCLog(t *testing.T, resumeID string, bridge vendor.Bridge) string {
	t.Helper()
	clk := clock.NewFake(testEpoch())
	adapter := newTestAdapter(t, clk)
	rpcLog := t.TempDir() + "/bridged-rpcs.jsonl"
	sess, err := adapter.Start(t.Context(), &vendor.StartSpec{
		SessionID:       "fugue-sess-bridge",
		Cwd:             t.TempDir(),
		Home:            vendor.AccountHome{ConfigDir: fakeRecordRPCsConfigDir + ":" + rpcLog},
		HostEnv:         adapter.HostEnv,
		ResumeSessionID: resumeID,
		SessionToken:    redact.NewSecret(testSessionValue),
		Bridge:          bridge,
	})
	if err != nil {
		t.Fatalf("Start(bridge=%+v): %v", bridge.Binary, err)
	}
	if _, cErr := sess.Close(t.Context(), vendor.CloseHostStop); cErr != nil {
		t.Errorf("Close: %v", cErr)
	}
	return rpcLog
}
