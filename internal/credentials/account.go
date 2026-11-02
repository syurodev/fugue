package credentials

import (
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
)

// Provider is a model provider.
type Provider string

// The providers of stage A.
const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
)

// Kind is how an account authenticates.
type Kind string

// KindAPIKey is billed per token. KindChatGPT uses the person's ChatGPT plan
// through Sign in with ChatGPT and never takes part in automatic failover.
const (
	KindAPIKey  Kind = "api_key"
	KindChatGPT Kind = "chatgpt"
)

// LimitWindow is one ceiling a provider puts on an account: a window that
// resets, not the context window. Label names the window, UsedPercent is how
// full it is and ResetsAt is a Unix second, or 0 when the reset is unknown.
type LimitWindow struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    int64   `json:"resetsAt,omitempty"`
}

// Account is what is known of one provider account. It never holds a secret:
// the credential is read through Book.APIKey or Book.ChatGPTTokens.
type Account struct {
	ID       string
	Provider Provider
	Kind     Kind
	Label    string
	// SignedIn is false once the provider refused the credential for good.
	SignedIn bool
	// ExhaustedUntil is when a real limit refusal says the account is usable
	// again; zero or past means not marked.
	ExhaustedUntil time.Time
	// CooldownUntil keeps the account out of selection without saying it
	// reached a limit; zero or past means not cooling down.
	CooldownUntil time.Time
	Usage         []LimitWindow
	// ObservedAt is when Usage was last updated; zero when it never was.
	ObservedAt time.Time
	CreatedAt  time.Time
}

// ChatGPTTokens are the tokens of one Sign in with ChatGPT registration. The
// client ID is stored per account and reused on every later sign-in of that
// account; it is not a secret.
type ChatGPTTokens struct {
	ClientID  string
	Access    redact.Secret
	Refresh   redact.Secret
	ID        redact.Secret
	ExpiresAt time.Time
	Scopes    []string
}

func validateKind(provider Provider, kind Kind) error {
	switch {
	case provider == ProviderAnthropic && kind == KindAPIKey:
		return nil
	case provider == ProviderOpenAI && (kind == KindAPIKey || kind == KindChatGPT):
		return nil
	default:
		// Claude.ai subscriptions are never used by the native runtime (M-03).
		return errcode.New(errcode.InvalidArgument, "this provider does not offer that way to sign in")
	}
}
