package claude

// Decoding of the Claude Code -p stream-json wire format (vendor-runtime
// §5.1), as recorded from claude 2.1.319: every line is one JSON event with a
// type discriminator; content blocks and deltas follow the Anthropic message
// shapes. Unknown fields are ignored, never a safety condition (VR-02).

import (
	"encoding/json"
	"strings"
)

// Envelope fields present on every event; the raw line is kept for notices.
type envelope struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	UUID            string `json:"uuid"`
	SessionID       string `json:"session_id"`
	ParentToolUseID string `json:"parent_tool_use_id"`
}

type wireInit struct {
	ClaudeCodeVersion     string   `json:"claude_code_version"`
	SessionID             string   `json:"session_id"`
	Cwd                   string   `json:"cwd"`
	Model                 string   `json:"model"`
	PermissionMode        string   `json:"permissionMode"`
	SlashCommands         []string `json:"slash_commands"`
	TerminalSlashCommands []string `json:"terminal_slash_commands"`
	Tools                 []string `json:"tools"`
}

type wirePermissionDenied struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	Message   string `json:"message"`
}

// wireBlock is one content block: text, thinking or tool_use.
type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type wireMessage struct {
	ID      string      `json:"id"`
	Model   string      `json:"model"`
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireAssistant struct {
	Message wireMessage `json:"message"`
}

// wireUser carries tool_result blocks or replayed user messages.
type wireUser struct {
	UUID     string `json:"uuid,omitempty"`
	IsReplay bool   `json:"isReplay,omitempty"`
	Message  struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type wireToolResult struct {
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type wireUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type wireResult struct {
	Subtype             string    `json:"subtype"`
	IsError             bool      `json:"is_error"`
	Result              string    `json:"result"`
	Usage               wireUsage `json:"usage"`
	TotalCostUSD        float64   `json:"total_cost_usd"`
	NumTurns            int       `json:"num_turns"`
	CredentialExpiresAt string    `json:"credential_expires_at"`
}

// rate_limit_event (vendor-runtime §5.1): the windows the CLI reports.
type wireWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resetsAt"`
	Status      string  `json:"status"`
}

type wireRateLimit struct {
	RateLimitInfo struct {
		Status         string         `json:"status"`
		ResetsAt       int64          `json:"resetsAt"`
		RateLimitType  string         `json:"rateLimitType"`
		UnifiedWindows unifiedWindows `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
}

type unifiedWindows struct {
	FiveHour *wireWindow `json:"five_hour"`
	SevenDay *wireWindow `json:"seven_day"`
}

// stream_event (partial messages, --include-partial-messages).
type wireDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

type wireStream struct {
	Event struct {
		Type         string     `json:"type"`
		Index        int        `json:"index"`
		ContentBlock *wireBlock `json:"content_block"`
		Delta        *wireDelta `json:"delta"`
	} `json:"event"`
	APIMessageID string `json:"api_message_id"`
}

// wireFileInput carries the fields the Edit, Write and MultiEdit tools send;
// the diff builder computes the file_change from them (transcript §4).
type wireFileInput struct {
	Path      string        `json:"file_path"`
	Content   string        `json:"content"`
	OldString string        `json:"old_string"`
	NewString string        `json:"new_string"`
	Edits     []editPairRaw `json:"edits"`
}

type editPairRaw struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// toolResultBlocks decodes the user content field and keeps only tool_result
// blocks: a user event the CLI echoes back without tool results is not part
// of the §4 mapping and becomes a notice in the caller.
func toolResultBlocks(raw json.RawMessage) []wireToolResult {
	var blocks []wireToolResult
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	out := blocks[:0]
	for _, b := range blocks {
		if b.ToolUseID != "" {
			out = append(out, b)
		}
	}
	return out
}

// toolResultText flattens a tool_result content field: a bare string or an
// array of {type,text} blocks.
func toolResultText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var out strings.Builder
	for i, b := range blocks {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(b.Text)
	}
	return out.String()
}
