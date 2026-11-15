package agy

// The ACP v1 normalizer: one pure per-session state machine that translates
// decoded JSON-RPC frames into transcript records, updates, partials, and
// session lifecycle observations. Every unknown notification or unmodeled
// update (including available_commands_update in F9.8a) becomes a
// notice(unsupported_event) with a raw ref — never silently dropped (VR-E08).
// _meta.antigravity is never inspected for safety or routing (VR-02).

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
)

// Update records a transcript item delta or insertion produced from ACP frames.
type Update = vendor.Update

// Init captures session metadata extracted from initialize and session/new responses.
type Init = vendor.Init

// Partial emits streaming text or thinking chunks before turn completion.
type Partial = vendor.Partial

// UsageData carries normalized token counts reported on prompt completion.
type UsageData = vendor.UsageData

// TurnEnd signals completion, cancellation, or failure of the active ACP turn.
type TurnEnd = vendor.TurnEnd

// Observation bundles normalized transcript updates emitted by Reader.
type Observation = vendor.Observation

// Turn outcomes (vendor-runtime §10).
const (
	OutcomeCompleted   = string(vendor.OutcomeCompleted)
	OutcomeInterrupted = string(vendor.OutcomeInterrupted)
	OutcomeFailed      = string(vendor.OutcomeFailed)
)

// Normalizer holds per-session ACP stream normalization state.
type Normalizer struct {
	mu           sync.Mutex
	codec        *Codec
	session      string
	turn         string
	prevTurn     string
	promptCID    string
	version      string
	model        string
	capabilities HandshakeCapabilities
	line         int
	items        map[string]*transcript.Item
	textBufs     map[string]string
	tools        map[string]*toolState
	diffs        map[string]bool
}

type toolState struct {
	name     string
	title    string
	status   string
	input    string
	output   string
	exitCode *int
}

// NewNormalizer builds a per-session Antigravity normalizer.
func NewNormalizer(sessionID string) *Normalizer {
	return NewNormalizerWithCodec(sessionID, NewCodec())
}

// NewNormalizerWithCodec builds a per-session Antigravity normalizer sharing codec.
func NewNormalizerWithCodec(sessionID string, codec *Codec) *Normalizer {
	if codec == nil {
		codec = NewCodec()
	}
	return &Normalizer{
		codec:    codec,
		session:  sessionID,
		items:    make(map[string]*transcript.Item),
		textBufs: make(map[string]string),
		tools:    make(map[string]*toolState),
		diffs:    make(map[string]bool),
	}
}

// BeginTurn activates a Fugue turn ID and optional prompt RPC correlation ID.
func (n *Normalizer) BeginTurn(turnID, promptCID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.prevTurn = n.turn
	n.turn = turnID
	n.promptCID = promptCID
}

// RollbackTurn restores the previous turn ID when a prompt send fails.
func (n *Normalizer) RollbackTurn() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turn = n.prevTurn
	n.prevTurn = ""
	n.promptCID = ""
}

// HandshakeCaps returns the §5.3 capability summary recorded from initialize.
func (n *Normalizer) HandshakeCaps() HandshakeCapabilities {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.capabilities
}

// Normalize decodes one newline-delimited JSON-RPC line and maps it to an Observation.
func (n *Normalizer) Normalize(line []byte) (Observation, error) {
	frame, err := n.codec.DecodeFrame(line)
	if err != nil {
		return Observation{}, err
	}
	return n.NormalizeFrame(&frame), nil
}

// NormalizeFrame maps an already-decoded Frame to an Observation.
func (n *Normalizer) NormalizeFrame(frame *Frame) Observation {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.line++
	switch frame.Kind {
	case FrameMalformed:
		return n.unsupported(n.lineRef(), "acp:malformed_json")
	case FrameResponse:
		return n.normalizeResponse(frame)
	case FrameNotification:
		return n.normalizeNotification(frame)
	case FrameServerRequest:
		return n.normalizeServerRequest(frame)
	default:
		return n.unsupported(n.lineRef(), "acp:"+string(frame.Kind))
	}
}

func (n *Normalizer) normalizeResponse(frame *Frame) Observation {
	method := frame.CorrelatedMethod
	if method == "" {
		method = inferResponseMethod(frame.Result)
	}
	if frame.Error != nil {
		if method == MethodSessionPrompt || (n.promptCID != "" && frame.CanonicalID == n.promptCID) {
			return n.finishPrompt("", nil)
		}
		return Observation{}
	}
	switch method {
	case MethodInitialize:
		return n.normalizeInitializeResponse(frame)
	case MethodSessionNew, MethodSessionLoad:
		return n.normalizeSessionStateResponse(frame, method)
	case MethodSessionPrompt:
		var resp PromptResult
		if json.Unmarshal(frame.Result, &resp) != nil {
			return n.unsupported(n.frameRef(frame), MethodSessionPrompt)
		}
		return n.finishPrompt(resp.StopReason, resp.Usage)
	case MethodSessionSetConfigOption:
		return Observation{}
	default:
		return Observation{}
	}
}

func (n *Normalizer) normalizeInitializeResponse(frame *Frame) Observation {
	var resp InitializeResult
	if json.Unmarshal(frame.Result, &resp) != nil {
		return n.unsupported(n.frameRef(frame), MethodInitialize)
	}
	if resp.AgentInfo != nil && resp.AgentInfo.Version != "" {
		n.version = resp.AgentInfo.Version
	}
	n.capabilities = resp.CapabilitiesSummary()
	if resp.ProtocolVersion != ProtocolVersion {
		return n.unsupported(n.frameRef(frame), MethodInitialize+":protocol_version")
	}
	return Observation{}
}

func (n *Normalizer) normalizeSessionStateResponse(frame *Frame, method string) Observation {
	var resp SessionStateResult
	if json.Unmarshal(frame.Result, &resp) != nil || resp.SessionID == "" {
		return n.unsupported(n.frameRef(frame), method)
	}
	var modelID string
	if resp.Models != nil {
		modelID = resp.Models.CurrentModelID
	}
	if modelID != "" {
		n.model = modelID
	}
	var modeID string
	if resp.Modes != nil {
		modeID = resp.Modes.CurrentModeID
	}
	return Observation{
		Init: &Init{
			Version:        n.version,
			SessionID:      resp.SessionID,
			Model:          modelID,
			PermissionMode: modeID,
		},
	}
}

func (n *Normalizer) finishPrompt(stopReason string, usage *PromptUsage) Observation {
	outcome := OutcomeFailed
	switch stopReason {
	case StopReasonCancelled:
		outcome = OutcomeInterrupted
	case StopReasonEndTurn, StopReasonMaxTokens, StopReasonRefusal:
		outcome = OutcomeCompleted
	}
	turnID := n.currentTurnID()
	n.turn = ""
	n.promptCID = ""
	var usageData UsageData
	if usage != nil {
		usageData = UsageData{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CacheTokens:  usage.CachedReadTokens,
		}
	}
	return Observation{
		TurnEnd: &TurnEnd{
			TurnID:  turnID,
			Outcome: outcome,
			Usage:   usageData,
		},
	}
}

func inferResponseMethod(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	if _, ok := fields["agentCapabilities"]; ok {
		return MethodInitialize
	}
	if _, ok := fields["sessionId"]; ok {
		return MethodSessionNew
	}
	if _, ok := fields["stopReason"]; ok {
		return MethodSessionPrompt
	}
	if _, ok := fields["configOptions"]; ok {
		return MethodSessionSetConfigOption
	}
	return ""
}

func (n *Normalizer) normalizeNotification(frame *Frame) Observation {
	if frame.Method != MethodSessionUpdate {
		return n.unsupported(n.lineRef(), "acp:"+frame.Method)
	}
	var params SessionUpdateParams
	if json.Unmarshal(frame.Params, &params) != nil || len(params.Update) == 0 {
		return n.unsupported(n.lineRef(), "acp:"+MethodSessionUpdate)
	}
	var wire UpdateWire
	if json.Unmarshal(params.Update, &wire) != nil || wire.SessionUpdate == "" {
		return n.unsupported(n.lineRef(), "acp:"+MethodSessionUpdate)
	}
	return n.normalizeUpdate(&wire)
}

func (n *Normalizer) normalizeServerRequest(frame *Frame) Observation {
	turnID := n.currentTurnID()
	if frame.Method == MethodSessionRequestPermission {
		id := n.itemID("perm", frame.CanonicalID)
		item := transcript.UnsupportedEvent(id, n.session, turnID, "acp:"+MethodSessionRequestPermission)
		item.Metadata = transcript.NoticeMetadata{Level: "warning", Code: "unsupported_event"}
		item.Content = transcript.NoticeContent{Text: "Antigravity requested permission, which Fugue cannot relay yet"}
		return Observation{Records: []transcript.Item{item}}
	}
	return n.unsupported(n.frameRef(frame), "acp:"+frame.Method)
}

func (n *Normalizer) unsupported(rawRefSuffix, rawRef string) Observation {
	id := n.itemID("notice", rawRefSuffix)
	item := transcript.UnsupportedEvent(id, n.session, n.currentTurnID(), rawRef)
	return Observation{Records: []transcript.Item{item}}
}

func (n *Normalizer) recordNew(
	key, turnID, itemID string,
	kind transcript.Kind,
	meta transcript.Metadata,
	content transcript.Content,
) Observation {
	item := transcript.Item{
		ID:        itemID,
		SessionID: n.session,
		TurnID:    turnID,
		Kind:      kind,
		Revision:  1,
		Metadata:  meta,
		Content:   content,
	}
	n.items[key] = &item
	return Observation{Records: []transcript.Item{item}}
}

func (n *Normalizer) updateExisting(
	existing *transcript.Item,
	meta transcript.Metadata,
	content transcript.Content,
) Observation {
	before := *existing
	existing.Revision++
	existing.Metadata = meta
	existing.Content = content
	return Observation{Updates: []Update{{Before: before, After: *existing}}}
}

func (n *Normalizer) currentTurnID() string {
	return n.turn
}

func (n *Normalizer) scopeKey() string {
	if n.turn != "" {
		return n.turn
	}
	return "replay"
}

func (n *Normalizer) lineRef() string {
	return fmt.Sprintf("line-%d", n.line)
}

func (n *Normalizer) frameRef(frame *Frame) string {
	if frame.CanonicalID != "" {
		return "req-" + strings.TrimSpace(frame.CanonicalID)
	}
	return n.lineRef()
}

func (n *Normalizer) itemID(prefix, suffix string) string {
	return fmt.Sprintf("agy-%s-%s", prefix, suffix)
}
