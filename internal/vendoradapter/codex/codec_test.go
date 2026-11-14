package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
)

func TestCodexFrameOverTheLimitFailsTyped(t *testing.T) {
	t.Parallel()
	c := NewCodec()

	// Inbound frame exceeding process.MaxFrameSize (1 MiB) fails typed with FrameTooLarge.
	oversized := bytes.Repeat([]byte("a"), process.MaxFrameSize+1)
	_, err := c.DecodeFrame(oversized)
	if errcode.CodeOf(err) != errcode.FrameTooLarge {
		t.Fatalf("DecodeFrame oversized err = %v, want frame_too_large", err)
	}

	// Outbound frame exceeding process.MaxFrameSize also fails typed with FrameTooLarge.
	hugeText := strings.Repeat("x", process.MaxFrameSize)
	_, _, err = c.NextRequest(MethodTurnStart, map[string]string{"text": hugeText})
	if errcode.CodeOf(err) != errcode.FrameTooLarge {
		t.Fatalf("NextRequest oversized err = %v, want frame_too_large", err)
	}
}

func TestCodexServerRequestsAreClassifiedAndCorrelated(t *testing.T) {
	t.Parallel()
	c := NewCodec()

	assertRequestAndNotificationClassification(t, c)
	assertServerRequestAndResponseClassification(t, c)
	assertMalformedFrameNormalizesToUnsupportedNotice(t, c)
}

func assertRequestAndNotificationClassification(t *testing.T, c *Codec) {
	t.Helper()
	reqID, rawReq, err := c.NextRequest(MethodInitialize, InitializeParams{
		ClientInfo: ClientInfo{Name: "fugue", Version: "0.1.0"},
	})
	if err != nil || reqID != 1 || !bytes.HasSuffix(rawReq, []byte("\n")) {
		t.Fatalf("NextRequest = (%d, %q, %v)", reqID, rawReq, err)
	}

	respFrame, err := c.DecodeFrame([]byte(`{"id":1,"result":{"userAgent":"codex_cli_rs/0.167.0"}}`))
	if err != nil || respFrame.Kind != FrameResponse || respFrame.CanonicalID != "1" || respFrame.CorrelatedMethod != MethodInitialize {
		t.Fatalf("DecodeFrame response = (%+v, %v)", respFrame, err)
	}

	notifFrame, err := c.DecodeFrame([]byte(`{"method":"turn/started","params":{"threadId":"th-1","turn":{"id":"t-1","status":"inProgress"}}}`))
	if err != nil || notifFrame.Kind != FrameNotification || notifFrame.Method != MethodTurnStarted || notifFrame.CanonicalID != "" {
		t.Fatalf("DecodeFrame notification = (%+v, %v)", notifFrame, err)
	}
}

func assertServerRequestAndResponseClassification(t *testing.T, c *Codec) {
	t.Helper()
	srvIntFrame, err := c.DecodeFrame([]byte(`{"id":0,"method":"item/commandExecution/requestApproval","params":{"threadId":"th-1","turnId":"t-1","itemId":"call-1","command":"rm -rf build"}}`))
	if err != nil || srvIntFrame.Kind != FrameServerRequest || srvIntFrame.Method != MethodCommandApproval || srvIntFrame.CanonicalID != "0" {
		t.Fatalf("DecodeFrame server request (int id) = (%+v, %v)", srvIntFrame, err)
	}

	srvStrFrame, err := c.DecodeFrame([]byte(`{"id":"srv-req-7","method":"item/tool/requestUserInput","params":{"threadId":"th-1","turnId":"t-1","itemId":"call-2","questions":[]}}`))
	if err != nil || srvStrFrame.Kind != FrameServerRequest || srvStrFrame.Method != MethodRequestUserInput || srvStrFrame.CanonicalID != "srv-req-7" {
		t.Fatalf("DecodeFrame server request (string id) = (%+v, %v)", srvStrFrame, err)
	}
	assertEncodedResponseMatchesID(t, c, srvIntFrame.ID)
}

func assertEncodedResponseMatchesID(t *testing.T, c *Codec, rawID json.RawMessage) {
	t.Helper()
	encodedResp, err := c.EncodeResponse(rawID, CommandApprovalResponse{Decision: DecisionDecline}, nil)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var wire wireMessage
	if uErr := json.Unmarshal(bytes.TrimSpace(encodedResp), &wire); uErr != nil {
		t.Fatalf("unmarshal encoded response: %v", uErr)
	}
	if string(wire.ID) != "0" || wire.Error != nil {
		t.Fatalf("unexpected encoded response: %+v", wire)
	}
}

func assertMalformedFrameNormalizesToUnsupportedNotice(t *testing.T, c *Codec) {
	t.Helper()
	malformed, err := c.DecodeFrame([]byte(`{not valid json`))
	if err != nil || malformed.Kind != FrameMalformed {
		t.Fatalf("malformed frame = (%+v, %v)", malformed, err)
	}
	n := NewNormalizerWithCodec("sess-1", c)
	obs := n.NormalizeFrame(&malformed)
	if len(obs.Records) != 1 || obs.Records[0].Kind != transcript.Notice || obs.Records[0].RawRef == "" {
		t.Fatalf("malformed frame observation = %+v, want notice(unsupported_event) with raw ref", obs)
	}
}

func TestCodexEncodeNotificationAndErrorResponse(t *testing.T) {
	t.Parallel()
	c := NewCodec()

	notif, err := c.EncodeNotification(MethodInitialized, map[string]any{})
	if err != nil {
		t.Fatalf("EncodeNotification: %v", err)
	}
	if !bytes.Contains(notif, []byte(`"method":"initialized"`)) {
		t.Fatalf("unexpected notification payload: %s", notif)
	}

	rpcErr := &RPCError{Code: -32000, Message: "relay declined by host"}
	if got := rpcErr.Error(); got != "codex rpc error -32000: relay declined by host" {
		t.Fatalf("RPCError.Error() = %q", got)
	}
	errResp, err := c.EncodeResponse(json.RawMessage(`42`), nil, rpcErr)
	if err != nil {
		t.Fatalf("EncodeResponse with error: %v", err)
	}
	if !bytes.Contains(errResp, []byte(`"code":-32000`)) {
		t.Fatalf("unexpected error response payload: %s", errResp)
	}
	if _, err := c.EncodeResponse(nil, map[string]any{}, nil); err == nil {
		t.Fatal("EncodeResponse with empty id succeeded, want error")
	}
	if _, err := c.EncodeNotification("", nil); err == nil {
		t.Fatal("EncodeNotification with empty method succeeded, want error")
	}
}
