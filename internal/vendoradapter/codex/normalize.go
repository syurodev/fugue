package codex

// The JSON-RPC app-server normalizer: one pure state machine per session that
// turns each stdio frame into transcript records, updates, and session
// observations (transcript §3, §4, vendor-runtime §6). It never writes storage,
// never reads a clock, and never panics on vendor output. Every unknown
// notification, server request, or field shape becomes a
// notice(unsupported_event) with a raw ref — never a silent drop (TR-06, VR-08).

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
)

// Update is a transcript item revision pair (before and after).
type Update = vendor.Update

// Init is the session initialization observation.
type Init = vendor.Init

// Partial is a live streaming delta observation.
type Partial = vendor.Partial

// UsageData holds per-turn token usage counts.
type UsageData = vendor.UsageData

// TurnEnd is the turn completion observation.
type TurnEnd = vendor.TurnEnd

// Window is a single rate-limit quota window.
type Window = vendor.Window

// RateLimit is a rate-limit snapshot observation.
type RateLimit = vendor.RateLimit

// ServerRequest is a vendor server request requiring user interaction.
type ServerRequest = vendor.ServerRequest

// Observation is the normalized output of one vendor stdio frame.
type Observation = vendor.Observation

// Turn outcomes (vendor-runtime §10).
const (
	OutcomeCompleted   = string(vendor.OutcomeCompleted)
	OutcomeInterrupted = string(vendor.OutcomeInterrupted)
	OutcomeFailed      = string(vendor.OutcomeFailed)
)

// Normalizer holds per-session stream normalization state.
type Normalizer struct {
	mu          sync.Mutex
	codec       *Codec
	session     string
	turn        string
	prevTurn    string
	codexTurn   string
	version     string
	model       string
	init        *Init
	line        int
	items       map[string]*transcript.Item
	deltas      map[string]string
	fileChanges map[string][]FileUpdateChange
	turnUsage   map[string]UsageData
}

// NewNormalizer builds a per-session Codex normalizer.
func NewNormalizer(sessionID string) *Normalizer {
	return NewNormalizerWithCodec(sessionID, NewCodec())
}

// NewNormalizerWithCodec builds a per-session Codex normalizer sharing codec.
func NewNormalizerWithCodec(sessionID string, codec *Codec) *Normalizer {
	if codec == nil {
		codec = NewCodec()
	}
	return &Normalizer{
		codec:       codec,
		session:     sessionID,
		items:       make(map[string]*transcript.Item),
		deltas:      make(map[string]string),
		fileChanges: make(map[string][]FileUpdateChange),
		turnUsage:   make(map[string]UsageData),
	}
}

// BeginTurn activates a Fugue turn ID before sending a turn request.
func (n *Normalizer) BeginTurn(turnID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.prevTurn = n.turn
	n.turn = turnID
}

// RollbackTurn restores the previous turn ID if a turn send fails.
func (n *Normalizer) RollbackTurn() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turn = n.prevTurn
	n.prevTurn = ""
}

// CurrentTurnID returns the active Fugue turn ID (or Codex turn ID).
func (n *Normalizer) CurrentTurnID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.currentTurnID()
}

// CodexTurnID returns the active Codex turn ID from turn/started.
func (n *Normalizer) CodexTurnID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.codexTurn
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
		return n.unsupported(n.lineRef(), "malformed_json")
	case FrameResponse:
		return n.normalizeResponse(frame)
	case FrameNotification:
		return n.normalizeNotification(frame)
	case FrameServerRequest:
		return n.normalizeServerRequest(frame)
	default:
		return n.unsupported(n.lineRef(), string(frame.Kind))
	}
}

func (n *Normalizer) normalizeResponse(frame *Frame) Observation {
	if frame.Error != nil {
		return Observation{}
	}
	method := frame.CorrelatedMethod
	if method == "" {
		method = inferResponseMethod(frame.Result)
	}
	switch method {
	case MethodInitialize:
		var resp InitializeResponse
		if json.Unmarshal(frame.Result, &resp) != nil || resp.UserAgent == "" {
			return n.unsupported(n.frameRef(frame), MethodInitialize)
		}
		n.version = parseUserAgentVersion(resp.UserAgent)
		return Observation{}
	case MethodThreadStart, MethodThreadResume:
		var resp ThreadStartResponse
		if json.Unmarshal(frame.Result, &resp) != nil || resp.Thread.ID == "" {
			return n.unsupported(n.frameRef(frame), method)
		}
		n.model = resp.Model
		initObs := &Init{
			Version:        n.version,
			SessionID:      resp.Thread.ID,
			Model:          resp.Model,
			PermissionMode: resp.ApprovalPolicy,
			SlashCommands:  SlashCommandNames(),
		}
		n.init = initObs
		return Observation{Init: initObs}
	case MethodRateLimitsRead:
		var resp RateLimitsReadResponse
		if json.Unmarshal(frame.Result, &resp) != nil {
			return n.unsupported(n.frameRef(frame), MethodRateLimitsRead)
		}
		return n.buildRateLimitObservation(resp.RateLimits, n.frameRef(frame))
	default:
		return Observation{}
	}
}

func inferResponseMethod(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	if _, ok := fields["userAgent"]; ok {
		return MethodInitialize
	}
	if _, ok := fields["rateLimits"]; ok {
		return MethodRateLimitsRead
	}
	if _, ok := fields["thread"]; ok {
		return MethodThreadStart
	}
	return ""
}

func parseUserAgentVersion(ua string) string {
	slash := strings.IndexByte(ua, '/')
	if slash < 0 {
		return ua
	}
	rest := ua[slash+1:]
	if space := strings.IndexByte(rest, ' '); space >= 0 {
		rest = rest[:space]
	}
	return strings.TrimSpace(rest)
}

func (n *Normalizer) normalizeNotification(frame *Frame) Observation {
	switch frame.Method {
	case MethodThreadStarted, MethodThreadNameUpdated, MethodServerRequestResolved:
		return n.normalizeLifecycleNotification(frame)
	case MethodTurnStarted:
		return n.normalizeTurnStarted(frame)
	case MethodTurnCompleted:
		return n.normalizeTurnCompleted(frame)
	case MethodTurnPlanUpdated:
		return n.normalizeTurnPlanUpdated(frame)
	case MethodTokenUsageUpdated:
		return n.normalizeTokenUsage(frame)
	case MethodRateLimitsUpdated:
		return n.normalizeRateLimitsUpdated(frame)
	case MethodError:
		return n.normalizeErrorNotification(frame)
	case MethodItemStarted:
		return n.normalizeItem(frame, false)
	case MethodItemCompleted:
		return n.normalizeItem(frame, true)
	default:
		return n.normalizeDeltaOrUnknown(frame)
	}
}

func (n *Normalizer) normalizeLifecycleNotification(frame *Frame) Observation {
	switch frame.Method {
	case MethodThreadStarted:
		var notif ThreadStartedNotification
		if json.Unmarshal(frame.Params, &notif) != nil || notif.Thread.ID == "" {
			return n.unsupported(n.lineRef(), frame.Method)
		}
		return Observation{}
	case MethodThreadNameUpdated:
		var notif ThreadNameUpdatedNotification
		if json.Unmarshal(frame.Params, &notif) != nil || notif.ThreadID == "" {
			return n.unsupported(n.lineRef(), frame.Method)
		}
		return Observation{}
	case MethodServerRequestResolved:
		var notif ServerRequestResolvedNotification
		if json.Unmarshal(frame.Params, &notif) != nil || notif.ThreadID == "" || len(notif.RequestID) == 0 {
			return n.unsupported(n.lineRef(), frame.Method)
		}
		return Observation{}
	default:
		return n.unsupported(n.lineRef(), frame.Method)
	}
}

func (n *Normalizer) normalizeTurnStarted(frame *Frame) Observation {
	var notif TurnStartedNotification
	if json.Unmarshal(frame.Params, &notif) != nil || notif.Turn.ID == "" {
		return n.unsupported(n.lineRef(), MethodTurnStarted)
	}
	n.codexTurn = notif.Turn.ID
	if n.turn == "" {
		n.turn = "vendor-" + notif.Turn.ID
	}
	return Observation{}
}

func (n *Normalizer) normalizeTurnCompleted(frame *Frame) Observation {
	var notif TurnCompletedNotification
	if json.Unmarshal(frame.Params, &notif) != nil || notif.Turn.ID == "" {
		return n.unsupported(n.lineRef(), MethodTurnCompleted)
	}
	turnID := n.resolveTurnID(notif.Turn.ID)
	end := &TurnEnd{
		TurnID: turnID,
		Usage:  n.turnUsage[turnID],
	}
	switch notif.Turn.Status {
	case TurnStatusCompleted:
		end.Outcome = OutcomeCompleted
	case TurnStatusInterrupted:
		end.Outcome = OutcomeInterrupted
	case TurnStatusFailed:
		end.Outcome = OutcomeFailed
		end.FailureCode = turnFailureCode(notif.Turn.Error)
	default:
		return n.unsupported(n.lineRef(), MethodTurnCompleted)
	}
	n.turn = ""
	n.codexTurn = ""
	return Observation{TurnEnd: end}
}

func turnFailureCode(errObj *TurnError) string {
	if errObj == nil {
		return "turn_failed"
	}
	var code string
	if len(errObj.CodexErrorInfo) > 0 && json.Unmarshal(errObj.CodexErrorInfo, &code) == nil && code != "" {
		return code
	}
	var keyed map[string]any
	if len(errObj.CodexErrorInfo) > 0 && json.Unmarshal(errObj.CodexErrorInfo, &keyed) == nil {
		for k := range keyed {
			return k
		}
	}
	return "turn_failed"
}

func (n *Normalizer) normalizeTokenUsage(frame *Frame) Observation {
	var notif TokenUsageUpdatedNotification
	if json.Unmarshal(frame.Params, &notif) != nil || notif.TurnID == "" {
		return n.unsupported(n.lineRef(), MethodTokenUsageUpdated)
	}
	turnID := n.resolveTurnID(notif.TurnID)
	src := notif.TokenUsage.Last
	if src.TotalTokens == 0 && notif.TokenUsage.Total.TotalTokens > 0 {
		src = notif.TokenUsage.Total
	}
	usage := UsageData{
		InputTokens:  src.InputTokens,
		OutputTokens: src.OutputTokens,
		CacheTokens:  src.CachedInputTokens,
	}
	n.turnUsage[turnID] = usage
	itemID := turnID + "-usage"
	meta := transcript.UsageMetadata{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CacheTokens:  usage.CacheTokens,
	}
	return n.recordOrUpdate(itemID, turnID, n.lineRef(), transcript.Usage, meta, transcript.UsageContent{})
}

func (n *Normalizer) normalizeRateLimitsUpdated(frame *Frame) Observation {
	var notif RateLimitsUpdatedNotification
	if json.Unmarshal(frame.Params, &notif) != nil {
		return n.unsupported(n.lineRef(), MethodRateLimitsUpdated)
	}
	return n.buildRateLimitObservation(notif.RateLimits, n.lineRef())
}

func (n *Normalizer) buildRateLimitObservation(snap RateLimitSnapshot, rawRef string) Observation {
	quota := &RateLimit{}
	if snap.Primary != nil {
		quota.Windows = append(quota.Windows, toQuotaWindow("primary", snap.Primary))
	}
	if snap.Secondary != nil {
		quota.Windows = append(quota.Windows, toQuotaWindow("secondary", snap.Secondary))
	}
	if len(quota.Windows) == 0 {
		return Observation{Quota: quota}
	}
	obs := Observation{Quota: quota}
	turnID := n.currentTurnID()
	for _, win := range quota.Windows {
		obs.Records = append(obs.Records, transcript.Item{
			ID:        "quota-" + rawRef + "-" + win.Name,
			SessionID: n.session,
			TurnID:    turnID,
			Kind:      transcript.Quota,
			RawRef:    rawRef,
			Metadata:  transcript.QuotaMetadata{Window: win.Name, Used: win.Utilization, ResetsAt: win.ResetsAt},
			Content:   transcript.QuotaContent{},
		})
	}
	return obs
}

func toQuotaWindow(name string, w *RateLimitWindow) Window {
	var resetsAt time.Time
	if w.ResetsAt != nil && *w.ResetsAt > 0 {
		resetsAt = time.Unix(*w.ResetsAt, 0).UTC()
	}
	return Window{
		Name:        name,
		Utilization: float64(w.UsedPercent) / 100.0,
		ResetsAt:    resetsAt,
	}
}

func (n *Normalizer) normalizeErrorNotification(frame *Frame) Observation {
	var notif ErrorNotification
	if json.Unmarshal(frame.Params, &notif) != nil || notif.Error.Message == "" {
		return n.unsupported(n.lineRef(), MethodError)
	}
	level := "error"
	if notif.WillRetry {
		level = "warning"
	}
	ref := n.lineRef()
	item := transcript.Item{
		ID:        "notice-" + ref,
		SessionID: n.session,
		TurnID:    n.resolveTurnID(notif.TurnID),
		Kind:      transcript.Notice,
		RawRef:    ref,
		Metadata:  transcript.NoticeMetadata{Level: level, Code: "codex_error"},
		Content:   transcript.NoticeContent{Text: notif.Error.Message},
	}
	return Observation{Records: []transcript.Item{item}}
}

func (n *Normalizer) currentTurnID() string {
	if n.turn != "" {
		return n.turn
	}
	if n.codexTurn != "" {
		return n.codexTurn
	}
	return ""
}

func (n *Normalizer) resolveTurnID(codexTurnID string) string {
	if n.turn != "" {
		return n.turn
	}
	if codexTurnID != "" {
		return codexTurnID
	}
	return n.codexTurn
}

func (n *Normalizer) lineRef() string {
	return fmt.Sprintf("line-%d", n.line)
}

func (n *Normalizer) frameRef(frame *Frame) string {
	if frame.CanonicalID != "" {
		return "req-" + frame.CanonicalID
	}
	return n.lineRef()
}

func (n *Normalizer) unsupported(rawRef, label string) Observation {
	if label == "" {
		label = "unknown"
	}
	item := transcript.UnsupportedEvent("notice-"+rawRef, n.session, n.currentTurnID(), rawRef)
	item.Content = transcript.NoticeContent{Text: label}
	return Observation{Records: []transcript.Item{item}}
}
