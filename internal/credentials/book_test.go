package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vault"
)

// marker is planted in every secret of these tests so a leak can be found by
// searching for it.
const (
	marker       = "MARKER-7f3a9c"
	testClientID = "client-1"
	windowName   = "requests"
)

// flatten shows a ChatGPTTokens value to cmp, which cannot look inside a Secret.
func flatten(t *ChatGPTTokens) []string {
	return append([]string{t.ClientID, t.Access.Reveal(), t.Refresh.Reveal(), t.ID.Reveal(), t.ExpiresAt.String()}, t.Scopes...)
}

func render(v any) string { return fmt.Sprintf("%v|%+v|%#v|%s", v, v, v, v) }

func chatgptTokens() *ChatGPTTokens {
	return &ChatGPTTokens{
		ClientID: testClientID, Access: redact.NewSecret("access-" + marker), Refresh: redact.NewSecret("refresh-" + marker),
		ID: redact.NewSecret("id-" + marker), ExpiresAt: time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC), Scopes: []string{"openid", "offline_access"},
	}
}

func TestAnAPIKeyRoundTripsThroughTheVault(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	account, err := f.book.AddAPIKey(ctx, ProviderAnthropic, " Work ", redact.NewSecret("sk-"+marker))
	if err != nil {
		t.Fatal(err)
	}
	want := Account{
		ID: "acct-1", Provider: ProviderAnthropic, Kind: KindAPIKey, Label: "Work", SignedIn: true,
		ExhaustedUntil: time.Time{}, CooldownUntil: time.Time{}, Usage: nil, ObservedAt: time.Time{}, CreatedAt: f.clock.Now(),
	}
	if diff := cmp.Diff(want, account); diff != "" {
		t.Fatalf("added account (-want +got):\n%s", diff)
	}
	got, err := f.book.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("stored account (-want +got):\n%s", diff)
	}
	key, err := f.book.APIKey(ctx, account.ID)
	if err != nil || key.Reveal() != "sk-"+marker {
		t.Fatalf("APIKey did not return the stored key (err %v)", err)
	}
	_, err = f.book.ChatGPTTokens(ctx, account.ID)
	wantCode(t, err, errcode.FailedPrecondition)
}

func TestNoSecretReachesTheDatabaseFormattingOrErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	key := redact.NewSecret("sk-" + marker)
	apiAccount, err := f.book.AddAPIKey(ctx, ProviderOpenAI, "key", key)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.book.AddChatGPT(ctx, "plan", chatgptTokens())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.book.ChatGPTTokens(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{render(key), render(apiAccount), render(plan), render(stored)}
	if encoded, marshalErr := json.Marshal(stored); marshalErr != nil {
		t.Fatal(marshalErr)
	} else {
		texts = append(texts, string(encoded))
	}
	f.vault.Lock()
	for _, id := range []string{apiAccount.ID, plan.ID} {
		_, apiErr := f.book.APIKey(ctx, id)
		_, tokenErr := f.book.ChatGPTTokens(ctx, id)
		texts = append(texts, render(apiErr), render(tokenErr))
	}
	_, addErr := f.book.AddAPIKey(ctx, ProviderAnthropic, "again", key)
	texts = append(texts, render(addErr))
	for i, text := range texts {
		if strings.Contains(text, marker) {
			t.Errorf("rendering %d shows a secret", i)
		}
	}
	raw, err := os.ReadFile(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("a secret is stored in the clear")
	}
}

func TestSecretsFailClosedWhileTheVaultIsLocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	account, err := f.book.AddAPIKey(ctx, ProviderAnthropic, "work", redact.NewSecret("sk-"+marker))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.book.AddChatGPT(ctx, "plan", chatgptTokens())
	if err != nil {
		t.Fatal(err)
	}
	f.vault.Lock()
	_, err = f.book.APIKey(ctx, account.ID)
	wantCode(t, err, errcode.FailedPrecondition)
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("APIKey error = %v, want vault.ErrLocked", err)
	}
	_, err = f.book.ChatGPTTokens(ctx, plan.ID)
	wantCode(t, err, errcode.FailedPrecondition)
	_, err = f.book.AddAPIKey(ctx, ProviderAnthropic, "late", redact.NewSecret("sk-"+marker))
	wantCode(t, err, errcode.FailedPrecondition)
	refresh := func(context.Context, ChatGPTTokens) (ChatGPTTokens, bool, error) {
		t.Error("a refresh ran while the vault was locked")
		return ChatGPTTokens{}, false, nil
	}
	_, err = f.book.RefreshChatGPT(ctx, plan.ID, refresh)
	wantCode(t, err, errcode.FailedPrecondition)
	// Metadata is not secret: accounts can still be listed and picked from.
	if accounts, listErr := f.book.List(ctx); listErr != nil || len(accounts) != 2 {
		t.Fatalf("List while locked = %d accounts, %v", len(accounts), listErr)
	}
}

func TestARowEditedInTheDatabaseCannotBeOpened(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	first, err := f.book.AddAPIKey(ctx, ProviderOpenAI, "first", redact.NewSecret("sk-first-"+marker))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.book.AddAPIKey(ctx, ProviderOpenAI, "second", redact.NewSecret("sk-second-"+marker))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, "UPDATE credential_accounts SET provider = 'anthropic' WHERE id = ?", first.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.book.APIKey(ctx, first.ID)
	wantCode(t, err, errcode.PermissionDenied)

	if _, err = f.db.ExecContext(ctx,
		"UPDATE credential_accounts SET secret = (SELECT secret FROM credential_accounts WHERE id = ?) WHERE id = ?",
		second.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, "UPDATE credential_accounts SET provider = 'openai' WHERE id = ?", first.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.book.APIKey(ctx, first.ID)
	wantCode(t, err, errcode.PermissionDenied)
}

func TestAddRejectsWhatTheProviderDoesNotOfferAndEmptyValues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	key := redact.NewSecret("sk-" + marker)
	cases := map[string]func() error{
		"unknown provider": func() error { _, err := f.book.AddAPIKey(ctx, "mistral", "x", key); return err },
		"empty key":        func() error { _, err := f.book.AddAPIKey(ctx, ProviderAnthropic, "x", redact.Secret{}); return err },
		"empty label":      func() error { _, err := f.book.AddAPIKey(ctx, ProviderAnthropic, "  ", key); return err },
		"long label": func() error {
			_, err := f.book.AddAPIKey(ctx, ProviderAnthropic, strings.Repeat("x", 201), key)
			return err
		},
		"chatgpt without id":    func() error { return addTokens(ctx, f, func(t *ChatGPTTokens) { t.ClientID = "" }) },
		"chatgpt without token": func() error { return addTokens(ctx, f, func(t *ChatGPTTokens) { t.Refresh = redact.Secret{} }) },
	}
	for name, add := range cases {
		wantCode(t, add(), errcode.InvalidArgument)
		if accounts, err := f.book.List(ctx); err != nil || len(accounts) != 0 {
			t.Fatalf("%s left %d accounts behind (%v)", name, len(accounts), err)
		}
	}
	if err := validateKind(ProviderAnthropic, KindChatGPT); err == nil {
		t.Fatal("Anthropic offers no sign-in with a subscription")
	}
}

func addTokens(ctx context.Context, f *fixture, edit func(*ChatGPTTokens)) error {
	tokens := chatgptTokens()
	edit(tokens)
	_, err := f.book.AddChatGPT(ctx, "plan", tokens)
	return err
}

func TestChatGPTRefreshStoresNewTokensAndKeepsTheClientID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	plan, err := f.book.AddChatGPT(ctx, "plan", chatgptTokens())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.book.SetSignedIn(ctx, plan.ID, false); err != nil {
		t.Fatal(err)
	}
	var seen []string
	renew := func(_ context.Context, current ChatGPTTokens) (ChatGPTTokens, bool, error) {
		seen = append(seen, current.Refresh.Reveal())
		next := current
		next.Refresh = redact.NewSecret("rotated-" + marker)
		next.Access = redact.NewSecret("fresh-" + marker)
		return next, true, nil
	}
	if _, err = f.book.RefreshChatGPT(ctx, plan.ID, renew); err != nil {
		t.Fatal(err)
	}
	if _, err = f.book.RefreshChatGPT(ctx, plan.ID, renew); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"refresh-" + marker, "rotated-" + marker}, seen); diff != "" {
		t.Fatalf("every refresh sees the tokens stored by the one before (-want +got):\n%s", diff)
	}
	stored, err := f.book.ChatGPTTokens(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := chatgptTokens()
	want.Access, want.Refresh = redact.NewSecret("fresh-"+marker), redact.NewSecret("rotated-"+marker)
	if diff := cmp.Diff(flatten(want), flatten(&stored)); diff != "" {
		t.Fatalf("stored tokens are not the refreshed ones (-want +got):\n%s", diff)
	}
	if account, getErr := f.book.Get(ctx, plan.ID); getErr != nil || !account.SignedIn {
		t.Fatalf("a refresh must mark the account signed in (err %v)", getErr)
	}
}

func TestChatGPTRefreshThatChangesNothingOrFailsStoresNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	plan, err := f.book.AddChatGPT(ctx, "plan", chatgptTokens())
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func(_ context.Context, current ChatGPTTokens) (ChatGPTTokens, bool, error) {
		return ChatGPTTokens{}, false, nil
	}
	got, err := f.book.RefreshChatGPT(ctx, plan.ID, unchanged)
	if err != nil || got.Refresh.Reveal() != "refresh-"+marker {
		t.Fatalf("an unchanged refresh must return the stored tokens (err %v)", err)
	}
	failing := func(context.Context, ChatGPTTokens) (ChatGPTTokens, bool, error) {
		return ChatGPTTokens{}, false, errcode.New(errcode.PermissionDenied, "invalid_grant")
	}
	_, err = f.book.RefreshChatGPT(ctx, plan.ID, failing)
	wantCode(t, err, errcode.PermissionDenied)
	otherClient := func(_ context.Context, current ChatGPTTokens) (ChatGPTTokens, bool, error) {
		current.ClientID = "client-2"
		return current, true, nil
	}
	_, err = f.book.RefreshChatGPT(ctx, plan.ID, otherClient)
	wantCode(t, err, errcode.InvalidArgument)
	emptied := func(context.Context, ChatGPTTokens) (ChatGPTTokens, bool, error) {
		return ChatGPTTokens{ClientID: testClientID, Access: redact.Secret{}, Refresh: redact.Secret{}, ID: redact.Secret{}, ExpiresAt: time.Time{}, Scopes: nil}, true, nil
	}
	_, err = f.book.RefreshChatGPT(ctx, plan.ID, emptied)
	wantCode(t, err, errcode.InvalidArgument)
	stored, err := f.book.ChatGPTTokens(ctx, plan.ID)
	if err != nil || stored.Refresh.Reveal() != "refresh-"+marker {
		t.Fatalf("a refresh that failed must leave the stored tokens alone (err %v)", err)
	}
	_, err = f.book.RefreshChatGPT(ctx, "missing", unchanged)
	wantCode(t, err, errcode.NotFound)
}

func TestARefreshHoldsTheLockOfItsAccountOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	unlock := f.book.lockAccount("a")
	held := func(id string) bool {
		f.book.mu.Lock()
		defer f.book.mu.Unlock()
		if f.book.locks[id].TryLock() {
			f.book.locks[id].Unlock()
			return false
		}
		return true
	}
	other := f.book.lockAccount("b")
	if !held("a") || !held("b") {
		t.Fatal("a lock that was taken must be held")
	}
	unlock()
	if held("a") || !held("b") {
		t.Fatal("releasing one account must not release another")
	}
	other()
}

func TestUsageIsMergedByLabelAndStaleReadingsAreIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	account, err := f.book.AddAPIKey(ctx, ProviderAnthropic, "work", redact.NewSecret("sk-"+marker))
	if err != nil {
		t.Fatal(err)
	}
	reset := f.clock.Now().Add(time.Hour).Unix()
	if err = f.book.SaveUsage(ctx, account.ID, []LimitWindow{{Label: windowName, UsedPercent: 40, ResetsAt: reset}}); err != nil {
		t.Fatal(err)
	}
	observed := f.clock.Now()
	f.clock.Advance(time.Minute)
	// Lower percentage of the same window arriving late: nothing changes, not even the observed time.
	if err = f.book.SaveUsage(ctx, account.ID, []LimitWindow{{Label: windowName, UsedPercent: 10, ResetsAt: reset}}); err != nil {
		t.Fatal(err)
	}
	if err = f.book.SaveUsage(ctx, account.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, err := f.book.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ObservedAt.Equal(observed) || len(got.Usage) != 1 || got.Usage[0].UsedPercent != 40 {
		t.Fatalf("a stale reading changed the account: %+v", got)
	}
	saveFresherUsage(t, f, account.ID, reset)
}

func saveFresherUsage(t *testing.T, f *fixture, id string, reset int64) {
	t.Helper()
	ctx := t.Context()
	if err := f.book.SaveUsage(ctx, id, []LimitWindow{
		{Label: windowName, UsedPercent: 55, ResetsAt: reset}, {Label: "tokens", UsedPercent: 5, ResetsAt: 0},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.book.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []LimitWindow{{Label: windowName, UsedPercent: 55, ResetsAt: reset}, {Label: "tokens", UsedPercent: 5, ResetsAt: 0}}
	if diff := cmp.Diff(want, got.Usage); diff != "" || !got.ObservedAt.Equal(f.clock.Now()) {
		t.Fatalf("usage (-want +got):\n%s\nobserved %v", diff, got.ObservedAt)
	}
	wantCode(t, f.book.SaveUsage(ctx, "missing", want), errcode.NotFound)
}

func TestMergeWindowsReplacesNewResetsAndNoResetReadings(t *testing.T) {
	t.Parallel()
	old := []LimitWindow{{Label: "a", UsedPercent: 80, ResetsAt: 100}, {Label: "b", UsedPercent: 80, ResetsAt: 0}}
	merged, accepted := mergeWindows(old, []LimitWindow{{Label: "a", UsedPercent: 5, ResetsAt: 200}, {Label: "b", UsedPercent: 5, ResetsAt: 0}})
	want := []LimitWindow{{Label: "a", UsedPercent: 5, ResetsAt: 200}, {Label: "b", UsedPercent: 5, ResetsAt: 0}}
	if diff := cmp.Diff(want, merged); diff != "" || !accepted {
		t.Fatalf("a new reset and a window without a known reset both replace (-want +got):\n%s", diff)
	}
	if old[0].UsedPercent != 80 {
		t.Fatal("the stored readings were modified in place")
	}
}

func TestMarkersOnlyMoveLater(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	account, err := f.book.AddAPIKey(ctx, ProviderAnthropic, "work", redact.NewSecret("sk-"+marker))
	if err != nil {
		t.Fatal(err)
	}
	later, sooner := f.clock.Now().Add(2*time.Hour), f.clock.Now().Add(time.Hour)
	for _, mark := range []func(context.Context, string, time.Time) error{f.book.MarkExhausted, f.book.MarkCooldown} {
		if err = mark(ctx, account.ID, later); err != nil {
			t.Fatal(err)
		}
		if err = mark(ctx, account.ID, sooner); err != nil {
			t.Fatal(err)
		}
		wantCode(t, mark(ctx, "missing", later), errcode.NotFound)
	}
	got, err := f.book.Get(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExhaustedUntil.Equal(later) || !got.CooldownUntil.Equal(later) {
		t.Fatalf("markers = %v and %v, want %v", got.ExhaustedUntil, got.CooldownUntil, later)
	}
}

func TestListIsOldestFirstAndDeleteRemovesTheAccount(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	added := make([]string, 0, 3)
	for _, label := range []string{"one", "two", "three"} {
		account, err := f.book.AddAPIKey(ctx, ProviderAnthropic, label, redact.NewSecret("sk-"+marker))
		if err != nil {
			t.Fatal(err)
		}
		added = append(added, account.ID)
		f.clock.Advance(time.Second)
	}
	if err := f.book.SetSignedIn(ctx, added[1], false); err != nil {
		t.Fatal(err)
	}
	accounts, err := f.book.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(accounts))
	for i := range accounts {
		labels = append(labels, fmt.Sprintf("%s:%t", accounts[i].Label, accounts[i].SignedIn))
	}
	if diff := cmp.Diff([]string{"one:true", "two:false", "three:true"}, labels); diff != "" {
		t.Fatalf("List (-want +got):\n%s", diff)
	}
	if err = f.book.Delete(ctx, added[1]); err != nil {
		t.Fatal(err)
	}
	_, err = f.book.Get(ctx, added[1])
	wantCode(t, err, errcode.NotFound)
	_, err = f.book.APIKey(ctx, added[1])
	wantCode(t, err, errcode.NotFound)
	wantCode(t, f.book.Delete(ctx, added[1]), errcode.NotFound)
	wantCode(t, f.book.SetSignedIn(ctx, added[1], true), errcode.NotFound)
}
