package claude

// The stream-json normalizer: one pure state machine per session that turns
// each stdout line into transcript records, updates and session observations
// (transcript §4, vendor-runtime §6). It never writes storage, never reads a
// clock and never panics on vendor output: the vendor stream is untrusted
// data. Every event it does not map becomes a notice(unsupported_event) with
// the raw event reference — never a silent drop (TR-06, VR-08).

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/transcript"
)

// Update pairs the stored item with its next revision.
type Update struct {
	Before transcript.Item
	After  transcript.Item
}

// Init is the system/init observation: identity, model, permission mode and
// the vendor's own slash command list with the terminal-only commands hidden
// (vendor-runtime §5.1, §9).
type Init struct {
	Version        string
	SessionID      string
	Model          string
	PermissionMode string
	SlashCommands  []string
}

// Partial is one stream_event delta, exposed as data: the session actor wires
// it to the transcript delta topic (transcript §5, F9.9).
type Partial struct {
	MessageID string
	BlockType string
	DeltaType string
	Fragment  string
}

// UsageData is the turn usage the result event reports.
type UsageData struct {
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
	Cost         float64
}

// TurnEnd closes the active turn: outcome, failure code, usage and the
// credential expiry the CLI reported, if any.
type TurnEnd struct {
	TurnID              string
	Outcome             string
	FailureCode         string
	Usage               UsageData
	CredentialExpiresAt *time.Time
}

// Window is one quota window of a rate_limit_event.
type Window struct {
	Name        string
	Utilization float64
	ResetsAt    time.Time
	Status      string
}

// RateLimit carries the full rate_limit_event data for the future quota owner
// (F9.10); the normalizer only reports it, it never acts on quota.
type RateLimit struct {
	Windows []Window
}

// Observation is everything one event normalized into.
type Observation struct {
	Init    *Init
	Records []transcript.Item
	Updates []Update
	Partial *Partial
	TurnEnd *TurnEnd
	Quota   *RateLimit
	// Nested counts events with a parent_tool_use_id (subagent streams). They
	// belong to the subagent item, not the session transcript; F9.5b/F9.9
	// enrich the subagent item with them.
	Nested int
}

// Turn outcomes (vendor-runtime §10).
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
)

// Normalizer is one session's stream state. The actor calls BeginTurn with the
// turn id it minted before sending the user line; events attach to the active
// turn and the result event closes it.
type Normalizer struct {
	session   string
	turn      string
	items     map[string]*transcript.Item
	msgCounts map[string]int
	init      *Init
	nested    int
	line      int
}

// NewNormalizer builds the per-session normalizer.
func NewNormalizer(sessionID string) *Normalizer {
	return &Normalizer{
		session:   sessionID,
		items:     map[string]*transcript.Item{},
		msgCounts: map[string]int{},
	}
}

// BeginTurn activates a turn; the actor calls it right before each Send.
func (n *Normalizer) BeginTurn(turnID string) { n.turn = turnID }

// SlashCommands returns the latest vendor slash command list, terminal-only
// commands hidden, vendor order kept (vendor-runtime §9).
func (n *Normalizer) SlashCommands() []string {
	if n.init == nil {
		return nil
	}
	return n.init.SlashCommands
}

// Normalize maps one stream-json line. A line that is not JSON at all is a
// protocol violation and fails typed; JSON the decoder does not recognize —
// wrong shapes included — becomes a notice(unsupported_event), never an error
// and never a panic.
func (n *Normalizer) Normalize(line []byte) (Observation, error) {
	n.line++
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Observation{}, errcode.Wrap(errcode.InvalidArgument,
			fmt.Sprintf("claude stream line %d is not JSON", n.line), err)
	}
	if env.ParentToolUseID != "" {
		n.nested++
		return Observation{Nested: n.nested}, nil
	}
	return n.normalizeTop(line, &env)
}

func (n *Normalizer) normalizeTop(line []byte, env *envelope) (Observation, error) {
	switch env.Type {
	case "system":
		return n.normalizeSystem(line, env), nil
	case "assistant":
		return n.normalizeAssistant(line, env), nil
	case roleUser:
		return n.normalizeUser(line, env), nil
	case "result":
		return n.normalizeResult(line, env), nil
	case "rate_limit_event":
		return n.normalizeRateLimit(line, env), nil
	case "stream_event":
		return n.normalizeStream(line, env), nil
	default:
		return n.unsupported(env, env.Type), nil
	}
}

// rawRef references the raw event payload (TR-05): the CLI's event uuid, or
// the line number when the event carries none.
func (n *Normalizer) rawRef(env *envelope) string {
	if env.UUID != "" {
		return env.UUID
	}
	return fmt.Sprintf("line-%d", n.line)
}

// unsupported turns an event the normalizer does not map into a notice with
// the raw reference (TR-06).
func (n *Normalizer) unsupported(env *envelope, label string) Observation {
	item := transcript.UnsupportedEvent("notice-"+n.rawRef(env), n.session, n.turn, n.rawRef(env))
	item.Content = transcript.NoticeContent{Text: label}
	return Observation{Records: []transcript.Item{item}}
}
