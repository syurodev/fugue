package codex

// F9.11 review: `mcpServer/elicitation/request` is Codex's general MCP
// elicitation channel as well as its own privileged approval gates. Fugue
// relays only the MCP tool-call gate — form mode, `_meta.codex_approval_kind
// = "mcp_tool_call"`, empty requested schema — and refuses every other
// elicitation through the unsupported path: a notice for the transcript and
// a JSON-RPC error response to Codex, never an accept (VR-05).

import (
	"testing"

	"github.com/syurodev/fugue/internal/transcript"
)

func TestCodexNonToolCallElicitationsGoUnsupported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
	}{
		{
			name: "form_with_required_property",
			// A forwarded MCP elicitation asking the user for input.
			line: `{"id":1,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"context7","mode":"form","message":"Enter your API key","requestedSchema":{"type":"object","properties":{"apiKey":{"type":"string"}},"required":["apiKey"]}}}`,
		},
		{
			name: "form_with_optional_property",
			line: `{"id":2,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"maps","mode":"form","message":"Pick a city","requestedSchema":{"type":"object","properties":{"city":{"type":"string"}}}}}`,
		},
		{
			name: "url_mode",
			line: `{"id":3,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"acme","mode":"url","message":"Open this link to continue","url":"https://acme.example/consent","elicitationId":"e-1"}}`,
		},
		{
			name: "openai_form_mode",
			line: `{"id":4,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"slides","mode":"openai/form","message":"Choose a layout","requestedSchema":{"type":"object","properties":{"layout":{"type":"string"}}}}}`,
		},
		{
			name: "tool_call_gate_without_meta",
			line: `{"id":5,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"fugue","mode":"form","message":"Allow the fugue MCP server to run tool \"fugue_test_echo\"?","requestedSchema":{"type":"object","properties":{}}}}`,
		},
		{
			name: "tool_call_gate_with_other_kind",
			line: `{"id":6,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"browser","mode":"form","message":"Sign in to continue","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"browser_auth"}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := NewNormalizer("sess-elicit-scope")
			n.BeginTurn("turn-1")
			assertUnsupportedElicitation(t, n, tc.name, tc.line)
		})
	}
}

// assertUnsupportedElicitation feeds one elicitation line and checks the
// unsupported path: no relay, exactly the notice, method named, raw ref kept.
func assertUnsupportedElicitation(t *testing.T, n *Normalizer, name, line string) {
	t.Helper()
	obs := feed(t, n, line)
	if obs.ServerRequest != nil {
		t.Fatalf("%s: relayed a non-tool-call elicitation: %+v", name, obs.ServerRequest)
	}
	if len(obs.Records) != 1 {
		t.Fatalf("%s: records = %d, want exactly the unsupported notice", name, len(obs.Records))
	}
	item := obs.Records[0]
	if item.Kind != transcript.Notice {
		t.Fatalf("%s: kind = %s, want notice", name, item.Kind)
	}
	meta := metaOf[transcript.NoticeMetadata](t, item.Metadata)
	if meta.Code != unsupportedEventCode {
		t.Fatalf("%s: metadata = %+v, want unsupported_event", name, meta)
	}
	content, ok := item.Content.(transcript.NoticeContent)
	if !ok || content.Text != MethodElicitationRequest {
		t.Fatalf("%s: notice content = %+v, want the method name", name, item.Content)
	}
	if item.RawRef == "" {
		t.Fatalf("%s: missing RawRef on the unsupported notice", name)
	}
}

func TestCodexToolCallGateStillRelays(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("sess-elicit-gate")
	n.BeginTurn("turn-1")
	obs := feed(t, n, `{"id":7,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"fugue","mode":"form","message":"Allow the fugue MCP server to run tool \"fugue_test_echo\"?","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","persist":["session","always"],"tool_name":"fugue_test_echo"}}}`)
	req := obs.ServerRequest
	if req == nil {
		t.Fatal("the mcp_tool_call gate did not relay")
	}
	want := []string{ElicitationOptionAccept, ElicitationOptionAcceptSession, ElicitationOptionAcceptAlways, ElicitationOptionDecline}
	if len(req.Options) != len(want) {
		t.Fatalf("options = %v, want %v", req.Options, want)
	}
	for i, id := range want {
		if req.Options[i] != id {
			t.Errorf("options[%d] = %q, want %q", i, req.Options[i], id)
		}
	}

	// A gate whose `_meta` offers no persistence scopes offers only the
	// one-time answers.
	n2 := NewNormalizer("sess-elicit-gate2")
	n2.BeginTurn("turn-1")
	obs2 := feed(t, n2, `{"id":8,"method":"mcpServer/elicitation/request","params":{"threadId":"th-1","turnId":"turn-1","serverName":"fugue","mode":"form","message":"Allow?","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call"}}}`)
	if obs2.ServerRequest == nil {
		t.Fatal("a gate without persist scopes did not relay")
	}
	if len(obs2.ServerRequest.Options) != 2 ||
		obs2.ServerRequest.Options[0] != ElicitationOptionAccept ||
		obs2.ServerRequest.Options[1] != ElicitationOptionDecline {
		t.Fatalf("options without persist = %v, want accept, decline", obs2.ServerRequest.Options)
	}
}
