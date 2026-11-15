package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
)

// FrameKind classifies a decoded newline-delimited JSON-RPC frame from `codex app-server`.
type FrameKind string

const (
	// FrameResponse is a response to a client-initiated JSON-RPC request.
	FrameResponse FrameKind = "response"
	// FrameNotification is a server-to-client notification without an id.
	FrameNotification FrameKind = "notification"
	// FrameServerRequest is a server-to-client request carrying both method and id.
	FrameServerRequest FrameKind = "server_request"
	// FrameMalformed is a non-JSON or structurally invalid line that becomes an unsupported_event notice.
	FrameMalformed FrameKind = "malformed"
)

// RPCError is a JSON-RPC error object on a response frame.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex rpc error %d: %s", e.Code, e.Message)
}

// Frame is one classified stdio frame from `codex app-server`.
type Frame struct {
	Kind             FrameKind
	ID               json.RawMessage
	CanonicalID      string
	CorrelatedMethod string
	Method           string
	Params           json.RawMessage
	Result           json.RawMessage
	Error            *RPCError
	Raw              []byte
}

type wireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// Codec is a pure, goroutine-free JSON-RPC 2.0 frame encoder/decoder over
// newline-delimited stdio lines bounded by process.MaxFrameSize.
type Codec struct {
	mu      sync.Mutex
	nextID  int64
	pending map[string]string
}

// NewCodec returns a new JSON-RPC codec.
func NewCodec() *Codec {
	return &Codec{
		pending: make(map[string]string),
	}
}

// NextRequest allocates the next client request ID, records its method for
// response correlation, and encodes the newline-delimited JSON frame.
func (c *Codec) NextRequest(method string, params any) (int64, []byte, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	frame, err := c.EncodeRequest(id, method, params)
	if err != nil {
		return 0, nil, err
	}
	return id, frame, nil
}

// EncodeRequest records request correlation for id and encodes a client request line.
func (c *Codec) EncodeRequest(id int64, method string, params any) ([]byte, error) {
	if method == "" {
		return nil, errcode.New(errcode.InvalidArgument, "codex request method is required")
	}
	idRaw, err := json.Marshal(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal codex request id", err)
	}
	rawParams := json.RawMessage(`{}`)
	if params != nil {
		b, mErr := json.Marshal(params)
		if mErr != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "marshal codex request params", mErr)
		}
		rawParams = b
	}
	line, err := encodeWireMessage(&wireMessage{
		ID:     idRaw,
		Method: method,
		Params: rawParams,
	})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.pending[CanonicalID(idRaw)] = method
	c.mu.Unlock()
	return line, nil
}

// EncodeNotification encodes a client notification line (no request id).
func (c *Codec) EncodeNotification(method string, params any) ([]byte, error) {
	if method == "" {
		return nil, errcode.New(errcode.InvalidArgument, "codex notification method is required")
	}
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "marshal codex notification params", err)
		}
		rawParams = b
	}
	return encodeWireMessage(&wireMessage{
		Method: method,
		Params: rawParams,
	})
}

// EncodeResponse encodes a client response to a server-initiated request.
func (c *Codec) EncodeResponse(id json.RawMessage, result any, rpcErr *RPCError) ([]byte, error) {
	if !hasJSONID(id) {
		return nil, errcode.New(errcode.InvalidArgument, "codex response id is required")
	}
	msg := wireMessage{ID: append(json.RawMessage(nil), id...)}
	if rpcErr != nil {
		msg.Error = rpcErr
		return encodeWireMessage(&msg)
	}
	rawResult, err := marshalResponseResult(result)
	if err != nil {
		return nil, err
	}
	msg.Result = rawResult
	return encodeWireMessage(&msg)
}

func marshalResponseResult(result any) (json.RawMessage, error) {
	if result == nil {
		return json.RawMessage(`{}`), nil
	}
	switch v := result.(type) {
	case json.RawMessage:
		if len(v) > 0 {
			return append(json.RawMessage(nil), v...), nil
		}
		return json.RawMessage(`{}`), nil
	case []byte:
		if len(v) > 0 {
			return append(json.RawMessage(nil), v...), nil
		}
		return json.RawMessage(`{}`), nil
	default:
		b, err := json.Marshal(result)
		if err != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "marshal codex response result", err)
		}
		return b, nil
	}
}

// ReadFrame reads one bounded newline-delimited frame from r and classifies it.
func (c *Codec) ReadFrame(r *process.LineReader) (Frame, error) {
	line, err := r.ReadLine()
	if err != nil {
		return Frame{}, errcode.Wrap(errcode.CodeOf(err), "read codex frame", err)
	}
	return c.DecodeFrame(line)
}

// DecodeFrame classifies a single JSON-RPC line and correlates client request IDs.
// A frame exceeding process.MaxFrameSize fails with errcode.FrameTooLarge.
// A malformed JSON line returns FrameMalformed so the normalizer records a
// notice(unsupported_event) with a raw ref instead of crashing or dropping it.
func (c *Codec) DecodeFrame(line []byte) (Frame, error) {
	trimmed := bytes.TrimRight(line, "\r\n")
	if len(trimmed) > process.MaxFrameSize {
		return Frame{}, errcode.New(
			errcode.FrameTooLarge,
			fmt.Sprintf("codex stdio frame exceeded the %d-byte limit", process.MaxFrameSize),
		)
	}
	rawCopy := append([]byte(nil), trimmed...)
	if len(bytes.TrimSpace(rawCopy)) == 0 {
		return Frame{Kind: FrameMalformed, Raw: rawCopy}, nil
	}
	msg, ok := decodeWireMessage(rawCopy)
	if !ok {
		return Frame{Kind: FrameMalformed, Raw: rawCopy}, nil
	}
	return c.classifyWireMessage(&msg, rawCopy), nil
}

func decodeWireMessage(rawCopy []byte) (wireMessage, bool) {
	var msg wireMessage
	if err := json.Unmarshal(rawCopy, &msg); err != nil {
		return wireMessage{}, false
	}
	return msg, true
}

func (c *Codec) classifyWireMessage(msg *wireMessage, rawCopy []byte) Frame {
	hasID := hasJSONID(msg.ID)
	switch {
	case msg.Method != "" && hasID:
		if !validIDToken(msg.ID) {
			return Frame{Kind: FrameMalformed, Raw: rawCopy}
		}
		return Frame{
			Kind:        FrameServerRequest,
			ID:          msg.ID,
			CanonicalID: CanonicalID(msg.ID),
			Method:      msg.Method,
			Params:      msg.Params,
			Raw:         rawCopy,
		}
	case msg.Method != "" && !hasID:
		return Frame{
			Kind:   FrameNotification,
			Method: msg.Method,
			Params: msg.Params,
			Raw:    rawCopy,
		}
	case msg.Method == "" && hasID && (len(msg.Result) > 0 || msg.Error != nil):
		if !validIDToken(msg.ID) {
			return Frame{Kind: FrameMalformed, Raw: rawCopy}
		}
		cid := CanonicalID(msg.ID)
		c.mu.Lock()
		correlated := c.pending[cid]
		delete(c.pending, cid)
		c.mu.Unlock()
		return Frame{
			Kind:             FrameResponse,
			ID:               msg.ID,
			CanonicalID:      cid,
			CorrelatedMethod: correlated,
			Result:           msg.Result,
			Error:            msg.Error,
			Raw:              rawCopy,
		}
	default:
		return Frame{Kind: FrameMalformed, Raw: rawCopy}
	}
}

func encodeWireMessage(msg *wireMessage) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal codex wire message", err)
	}
	if bytes.IndexByte(data, '\n') >= 0 {
		return nil, errcode.New(errcode.InvalidArgument, "codex wire message must not contain a newline")
	}
	if len(data) > process.MaxFrameSize {
		return nil, errcode.New(
			errcode.FrameTooLarge,
			fmt.Sprintf("codex outbound frame exceeded the %d-byte limit", process.MaxFrameSize),
		)
	}
	return append(data, '\n'), nil
}

func hasJSONID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func validIDToken(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s != ""
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(&n) == nil
}

// CanonicalID normalizes a JSON-RPC request ID (integer or string) into a
// deterministic string key for correlation and deduplication.
func CanonicalID(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	var s string
	if json.Unmarshal(trimmed, &s) == nil {
		return s
	}
	var v any
	if json.Unmarshal(trimmed, &v) != nil {
		return string(trimmed)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(trimmed)
	}
	return string(out)
}
