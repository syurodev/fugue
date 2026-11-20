package hostapi

// F9.11 evidence: with the fugue bridge attached to a Codex or an agy
// session, the session token is in neither argv nor any file nor the logs —
// the vendor CLI gets it only in its environment and hands it to the bridge
// by name (Codex `env_vars`) or by inheritance (agy) — and the bridge serves
// the session's tools with no approve relay (VR-05: Codex and agy approvals
// arrive as server requests, never as an MCP tool).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
)

// vendorFakeLink symlinks the test binary under a CLI's file name, so the
// fake reports that vendor's tested version.
func vendorFakeLink(t *testing.T, name string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func vendorEnumOf(name string) vendorsv1.Vendor {
	if name == "agy" {
		return vendorsv1.Vendor_VENDOR_AGY
	}
	return vendorsv1.Vendor_VENDOR_CODEX
}

// TestBridgeTokenNeverInArgvFilesOrLogs (F9.11 evidence): for both vendors
// the fake CLI reports its startup — the token is only in its environment —
// and runs the real `fugue mcp-bridge` the way its vendor builds the child;
// the token is in neither argv, nor any file under the data directory, the
// Fugue home or the vendor's working files, nor in the host's log.
func TestBridgeTokenNeverInArgvFilesOrLogs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name string }{{name: "codex"}, {name: "agy"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logs := &lockedBuffer{}
			previous := slog.Default()
			slog.SetDefault(slog.New(redact.NewJSONHandler(logs, slog.LevelDebug)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			token := sessionTokens(t, 1)[0]
			// Registered before the host starts, so it runs after the host's
			// drain and close: nothing writes to logs while it is read.
			t.Cleanup(func() {
				if strings.Contains(logs.String(), token) {
					t.Error("the session token leaked into the host log")
				}
			})

			h := startBridgeTokenHost(t, buildFugue(t))
			sessionID, root := startBridgedTokenSession(t, h, tc.name)

			assertVendorStartup(t, h, sessionID, root, token)
			assertBridgeConversation(t, h, sessionID, root)
			// Nowhere on disk and nowhere in the log: the data directory
			// holds the store and the journal, the Fugue home the
			// configuration, and the session root every file the fake vendor
			// wrote, its own data files included.
			if hits := filesContaining(t, token, h.dataDir, h.fugueHome, root); len(hits) != 0 {
				t.Fatalf("the session token is on disk in %v", hits)
			}
		})
	}
}

// startBridgeTokenHost starts a host whose bridge is the real fugue binary
// and whose vendor CLIs are the fake test binary under each CLI's name.
func startBridgeTokenHost(t *testing.T, bridgeBinary string) *vendorHost {
	t.Helper()
	return startVendorHostWith(t, true, func(deps *app.HostDeps, sockPath string) {
		deps.Random = ids.NewDeterministic(sessionTokenSeed)
		deps.Bridge = app.VendorBridge{Binary: bridgeBinary, Socket: sockPath}
		deps.TestFugueTools = []*app.FugueTool{echoTool()}
		deps.CodexBinary = vendorFakeLink(t, "codex")
		deps.AgyBinary = vendorFakeLink(t, "agy")
	})
}

// startBridgedTokenSession starts a Codex or agy session in its own
// workspace root and returns the session id and root.
func startBridgedTokenSession(t *testing.T, v *vendorHost, vendor string) (string, string) {
	t.Helper()
	root := t.TempDir()
	wsID := v.workspaceWithRoot(t, root)
	stream := subscribeVendorSessions(t, v.host, nil)
	defer closeQuietly(stream)
	started, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{
		CommandId: "leak-" + vendor + "-start", Vendor: vendorEnumOf(vendor), WorkspaceId: wsID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, started.Msg.Session.SessionId, "ready")
	return started.Msg.Session.SessionId, root
}

// assertVendorStartup checks the fake vendor CLI's startup report: the token
// is only in its environment.
func assertVendorStartup(t *testing.T, v *vendorHost, sessionID, root, token string) {
	t.Helper()
	raw := v.runFakeAction(t, sessionID, root, fakeVendorReportPrompt, fakeVendorReportFile)
	var report fakeVendorStartup
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("startup report %q is undecodable", raw)
	}
	sum := sha256.Sum256([]byte(token))
	if report.TokenSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("the vendor process did not receive the session token in its environment")
	}
	if strings.Contains(strings.Join(report.Args, "\x00"), token) {
		t.Fatal("the session token is in the vendor argv")
	}
	for _, key := range report.EnvKeys {
		if strings.HasPrefix(key, "FUGUE_") && key != app.SessionTokenEnv {
			t.Errorf("the vendor environment carries %s", key)
		}
	}
}

// assertBridgeConversation runs the fugue MCP server the way the vendor
// builds the child and checks what the bridge served: the session's tools,
// no approve relay, and the echo call of the session.
func assertBridgeConversation(t *testing.T, v *vendorHost, sessionID, root string) {
	t.Helper()
	runRaw := v.runFakeAction(t, sessionID, root, fakeVendorBridgePrompt, fakeVendorBridgeRunFile)
	var run fakeVendorBridgeOutcome
	if json.Unmarshal(runRaw, &run) != nil || run.Error != "" || run.Exit != 0 || len(run.Replies) != len(fakeVendorBridgeRequests()) {
		t.Fatalf("bridge run = %s", runRaw)
	}
	assertBridgeToolList(t, run, sessionID)
}

// assertBridgeToolList checks the bridge's tools/list, the echo call of the
// session, and the refused approve relay.
func assertBridgeToolList(t *testing.T, run fakeVendorBridgeOutcome, sessionID string) {
	t.Helper()
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(run.Replies[1], &list); err != nil {
		t.Fatal(err)
	}
	// Teams default on (F11.2a), so the propose tool is listed; the approve
	// relay never is for Codex and agy (VR-05).
	names := make([]string, 0, len(list.Result.Tools))
	for _, tool := range list.Result.Tools {
		names = append(names, tool.Name)
	}
	if diff := cmp.Diff([]string{testProposeTool, testEchoTool}, names); diff != "" {
		t.Fatalf("tools/list names (-want +got):\n%s", diff)
	}
	var call struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(run.Replies[2], &call); err != nil {
		t.Fatal(err)
	}
	if call.Result.IsError || len(call.Result.Content) != 1 ||
		call.Result.Content[0].Text != "echo:"+sessionID+`:{"msg":"hi"}` {
		t.Fatalf("tools/call = %s", run.Replies[2])
	}
	var approve struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(run.Replies[3], &approve); err != nil || !approve.Result.IsError {
		t.Fatalf("approve over the bridge = %s, want an error result", run.Replies[3])
	}
}
