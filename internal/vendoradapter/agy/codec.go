package agy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
)

const jsonRPCVersion = "2.0"

// FrameKind classifies a decoded newline-delimited ACP JSON-RPC 2.0 frame.
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

// RPCError is a JSON-RPC 2.0 error object on a response frame.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("agy rpc error %d: %s", e.Code, e.Message)
}

// Frame is one classified stdio frame from `agy acp`.
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
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Codec is a goroutine-safe ACP JSON-RPC 2.0 encoder/decoder over
// newline-delimited stdio lines bounded by process.MaxFrameSize.
type Codec struct {
	mu      sync.Mutex
	nextID  int64
	pending map[string]string
}

// NewCodec returns a new ACP JSON-RPC 2.0 codec.
func NewCodec() *Codec {
	return &Codec{pending: make(map[string]string)}
}

// NextRequest allocates a client request ID, records correlation for method,
// and encodes a single newline-delimited JSON-RPC 2.0 request frame.
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

// EncodeRequest records correlation for id and encodes a JSON-RPC 2.0 request line.
func (c *Codec) EncodeRequest(id int64, method string, params any) ([]byte, error) {
	if method == "" {
		return nil, errcode.New(errcode.InvalidArgument, "agy request method is required")
	}
	idRaw, err := json.Marshal(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal agy request id", err)
	}
	rawParams := json.RawMessage(`{}`)
	if params != nil {
		b, mErr := json.Marshal(params)
		if mErr != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "marshal agy request params", mErr)
		}
		rawParams = b
	}
	line, err := encodeWireMessage(&wireMessage{
		JSONRPC: jsonRPCVersion,
		ID:      idRaw,
		Method:  method,
		Params:  rawParams,
	})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.pending[CanonicalID(idRaw)] = method
	c.mu.Unlock()
	return line, nil
}

// EncodeNotification encodes a JSON-RPC 2.0 notification line without an id.
func (c *Codec) EncodeNotification(method string, params any) ([]byte, error) {
	if method == "" {
		return nil, errcode.New(errcode.InvalidArgument, "agy notification method is required")
	}
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "marshal agy notification params", err)
		}
		rawParams = b
	}
	return encodeWireMessage(&wireMessage{
		JSONRPC: jsonRPCVersion,
		Method:  method,
		Params:  rawParams,
	})
}

// EncodeResponseResult encodes a JSON-RPC 2.0 result response to a server request.
func (c *Codec) EncodeResponseResult(id json.RawMessage, result any) ([]byte, error) {
	if !hasJSONID(id) {
		return nil, errcode.New(errcode.InvalidArgument, "agy response id is required")
	}
	rawResult := json.RawMessage(`{}`)
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return nil, errcode.Wrap(errcode.InvalidArgument, "marshal agy response result", err)
		}
		rawResult = b
	}
	return encodeWireMessage(&wireMessage{
		JSONRPC: jsonRPCVersion,
		ID:      append(json.RawMessage(nil), id...),
		Result:  rawResult,
	})
}

// EncodeResponseError encodes a JSON-RPC 2.0 error response to a server request.
func (c *Codec) EncodeResponseError(id json.RawMessage, code int, message string) ([]byte, error) {
	if !hasJSONID(id) {
		return nil, errcode.New(errcode.InvalidArgument, "agy response id is required")
	}
	return encodeWireMessage(&wireMessage{
		JSONRPC: jsonRPCVersion,
		ID:      append(json.RawMessage(nil), id...),
		Error:   &RPCError{Code: code, Message: message},
	})
}

// ReadFrame reads one bounded newline-delimited frame from r and classifies it.
func (c *Codec) ReadFrame(r *process.LineReader) (Frame, error) {
	line, err := r.ReadLine()
	if err != nil {
		return Frame{}, errcode.Wrap(errcode.CodeOf(err), "read agy frame", err)
	}
	return c.DecodeFrame(line)
}

// DecodeFrame classifies a single ACP JSON-RPC line and correlates response IDs.
// Any frame larger than process.MaxFrameSize fails with errcode.FrameTooLarge.
func (c *Codec) DecodeFrame(line []byte) (Frame, error) {
	trimmed := bytes.TrimRight(line, "\r\n")
	if len(trimmed) > process.MaxFrameSize {
		return Frame{}, errcode.New(
			errcode.FrameTooLarge,
			fmt.Sprintf("agy stdio frame exceeded the %d-byte limit", process.MaxFrameSize),
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
	if json.Unmarshal(rawCopy, &msg) != nil {
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
		return nil, errcode.Wrap(errcode.Internal, "marshal agy wire message", err)
	}
	if bytes.IndexByte(data, '\n') >= 0 {
		return nil, errcode.New(errcode.InvalidArgument, "agy wire message must not contain a newline")
	}
	if len(data) > process.MaxFrameSize {
		return nil, errcode.New(
			errcode.FrameTooLarge,
			fmt.Sprintf("agy outbound frame exceeded the %d-byte limit", process.MaxFrameSize),
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
// deterministic string key for request/response correlation.
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
