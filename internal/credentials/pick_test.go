package credentials

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func pickNow() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }

type vectorWindow struct {
	Label    string  `json:"label"`
	Used     float64 `json:"used"`
	ResetsIn int64   `json:"resetsIn"`
}

type vectorAccount struct {
	ID          string         `json:"id"`
	SignedIn    bool           `json:"signedIn"`
	ExhaustedIn int64          `json:"exhaustedIn"`
	CooldownIn  int64          `json:"cooldownIn"`
	Usage       []vectorWindow `json:"usage"`
}

type vectorPolicy struct {
	Enabled           bool           `json:"enabled"`
	Threshold         int            `json:"threshold"`
	WeeklyThreshold   int            `json:"weeklyThreshold"`
	Order             []string       `json:"order"`
	Strategy          string         `json:"strategy"`
	AccountThresholds map[string]int `json:"accountThresholds"`
}

type vectorWant struct {
	Target     string `json:"target"`
	Switch     bool   `json:"switch"`
	Reason     string `json:"reason"`
	AllLimited bool   `json:"allLimited"`
	RetryIn    int64  `json:"retryIn"`
}

type vectorCase struct {
	Policy      vectorPolicy    `json:"policy"`
	Accounts    []vectorAccount `json:"accounts"`
	Current     string          `json:"current"`
	SwitchedAgo int64           `json:"switchedAgo"`
	Previous    string          `json:"previous"`
	Model       string          `json:"model"`
	Want        vectorWant      `json:"want"`
}

func offset(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return pickNow().Add(time.Duration(seconds) * time.Second)
}

func (c *vectorCase) input() *PickInput {
	in := &PickInput{
		Policy: Policy{
			Enabled: c.Policy.Enabled, ThresholdPercent: c.Policy.Threshold, WeeklyThresholdPercent: c.Policy.WeeklyThreshold,
			Order: c.Policy.Order, Strategy: Strategy(c.Policy.Strategy), AccountThresholds: c.Policy.AccountThresholds,
		},
		Current: c.Current, Now: pickNow(), Previous: c.Previous, Model: c.Model,
	}
	if c.SwitchedAgo != 0 {
		in.SwitchedAt = offset(-c.SwitchedAgo)
	}
	for _, a := range c.Accounts {
		account := Account{
			ID: a.ID, Provider: ProviderAnthropic, Kind: KindAPIKey, Label: "L" + a.ID, SignedIn: a.SignedIn,
			ExhaustedUntil: offset(a.ExhaustedIn), CooldownUntil: offset(a.CooldownIn),
		}
		for _, w := range a.Usage {
			window := LimitWindow{Label: w.Label, UsedPercent: w.Used, ResetsAt: 0}
			if w.ResetsIn != 0 {
				window.ResetsAt = pickNow().Unix() + w.ResetsIn
			}
			account.Usage = append(account.Usage, window)
		}
		in.Accounts = append(in.Accounts, account)
	}
	return in
}

func (c *vectorCase) want() PickDecision {
	return PickDecision{Target: c.Want.Target, Switch: c.Want.Switch, Reason: c.Want.Reason, AllLimited: c.Want.AllLimited, RetryAt: offset(c.Want.RetryIn)}
}

// The vectors were produced by running dev-kit's agenthub.PickAccount over
// 220 generated inputs, covering strategies, thresholds, model windows, flip
// hold, return margin, signed-out and cooling accounts (docs/ports.md).
func TestPickAccountMatchesTheOriginalVectors(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/pick-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []vectorCase
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 220 {
		t.Fatalf("vectors = %d, want 220", len(cases))
	}
	for i := range cases {
		got := PickAccount(cases[i].input())
		if diff := cmp.Diff(cases[i].want(), got); diff != "" {
			t.Errorf("vector %d (-want +got):\n%s", i, diff)
		}
	}
}

type pickOpt func(*Account)

func openAI(kind Kind) pickOpt {
	return func(a *Account) { a.Provider, a.Kind = ProviderOpenAI, kind }
}

func exhaustedFor(d time.Duration) pickOpt {
	return func(a *Account) { a.ExhaustedUntil = pickNow().Add(d) }
}

func coolingFor(d time.Duration) pickOpt {
	return func(a *Account) { a.CooldownUntil = pickNow().Add(d) }
}

func pickAccount(id string, opts ...pickOpt) Account {
	a := Account{ID: id, Provider: ProviderAnthropic, Kind: KindAPIKey, Label: id, SignedIn: true}
	for _, opt := range opts {
		opt(&a)
	}
	return a
}

func pickFrom(current string, order []string, accounts ...Account) PickDecision {
	return PickAccount(&PickInput{Policy: Policy{Enabled: true, Order: order}, Accounts: accounts, Current: current, Now: pickNow()})
}

func TestAChatGPTPlanAccountNeverFailsOver(t *testing.T) {
	t.Parallel()
	plan := pickAccount("plan", openAI(KindChatGPT), exhaustedFor(time.Hour))
	key := pickAccount("key", openAI(KindAPIKey))
	spare := pickAccount("spare", openAI(KindAPIKey))
	got := pickFrom("plan", nil, plan, key, spare)
	want := PickDecision{Target: "plan", Switch: false, Reason: "", AllLimited: false, RetryAt: time.Time{}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("a refused ChatGPT plan must wait for the person (-want +got):\n%s", diff)
	}
}

func TestAChatGPTPlanAccountIsNeverAFailoverTarget(t *testing.T) {
	t.Parallel()
	first := pickAccount("first", openAI(KindAPIKey), exhaustedFor(time.Hour))
	plan := pickAccount("plan", openAI(KindChatGPT))
	second := pickAccount("second", openAI(KindAPIKey))
	got := pickFrom("first", []string{"plan", "second", "first"}, first, plan, second)
	if !got.Switch || got.Target != "second" {
		t.Fatalf("decision = %+v, want a switch to the other API key", got)
	}
	if only := pickFrom("first", nil, first, plan); only.Switch || only.AllLimited || only.Target != "first" {
		t.Fatalf("with no other API key the session stays and nothing is AllLimited: %+v", only)
	}
}

func TestFailoverStaysWithinOneProvider(t *testing.T) {
	t.Parallel()
	anthropic := pickAccount("anthropic", exhaustedFor(time.Hour))
	openai := pickAccount("openai", openAI(KindAPIKey))
	if got := pickFrom("anthropic", nil, anthropic, openai); got.Switch || got.AllLimited {
		t.Fatalf("another provider is no failover target: %+v", got)
	}
	sibling := pickAccount("sibling")
	if got := pickFrom("anthropic", nil, anthropic, openai, sibling); !got.Switch || got.Target != "sibling" {
		t.Fatalf("decision = %+v, want a switch to the sibling API key", got)
	}
}

func TestAllLimitedReportsTheEarliestReturn(t *testing.T) {
	t.Parallel()
	a := pickAccount("a", exhaustedFor(3*time.Hour))
	b := pickAccount("b", exhaustedFor(time.Hour))
	got := pickFrom("a", nil, a, b)
	if !got.AllLimited || got.Switch || !got.RetryAt.Equal(pickNow().Add(time.Hour)) {
		t.Fatalf("decision = %+v, want AllLimited with the return of b", got)
	}
}

func TestAnAlternativeThatIsCoolingDownIsNotAllLimited(t *testing.T) {
	t.Parallel()
	a := pickAccount("a", exhaustedFor(time.Hour))
	b := pickAccount("b", coolingFor(time.Hour))
	got := pickFrom("a", nil, a, b)
	if got.AllLimited || got.Switch || got.Target != "a" || got.Reason != "usage limit reached" {
		t.Fatalf("decision = %+v, want to stay on the refused account", got)
	}
}
