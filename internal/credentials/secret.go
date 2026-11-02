package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	recordTypePrefix = "credentials.account."
	recordKeyID      = "v1"
)

// Refresher renews the tokens of one account. It receives the tokens as they are
// stored now, so a refresh that another caller completed first is visible, and
// reports whether it produced new ones.
type Refresher func(ctx context.Context, current ChatGPTTokens) (next ChatGPTTokens, changed bool, err error)

// chatgptRecord is the sealed form of ChatGPTTokens.
type chatgptRecord struct {
	ClientID  string   `json:"clientId"`
	Access    string   `json:"access"`
	Refresh   string   `json:"refresh"`
	ID        string   `json:"id"`
	ExpiresAt int64    `json:"expiresAt"`
	Scopes    []string `json:"scopes"`
}

// binding authenticates the record's complete scope, so a row whose provider,
// kind or id was edited cannot be opened.
func binding(provider Provider, kind Kind, id string) vault.RecordBinding {
	return vault.RecordBinding{RecordType: recordTypePrefix + string(provider) + "." + string(kind), RecordID: id, KeyID: recordKeyID}
}

// AddAPIKey adds an account that authenticates with an API key.
func (b *Book) AddAPIKey(ctx context.Context, provider Provider, label string, key redact.Secret) (Account, error) {
	if key.IsZero() {
		return Account{}, errcode.New(errcode.InvalidArgument, "api key is empty")
	}
	return b.add(ctx, provider, KindAPIKey, label, []byte(key.Reveal()))
}

// AddChatGPT adds an OpenAI account that signed in with ChatGPT.
func (b *Book) AddChatGPT(ctx context.Context, label string, tokens *ChatGPTTokens) (Account, error) {
	plaintext, err := encodeTokens(tokens)
	if err != nil {
		return Account{}, err
	}
	return b.add(ctx, ProviderOpenAI, KindChatGPT, label, plaintext)
}

// APIKey returns the key of an API-key account, for the adapter's request.
func (b *Book) APIKey(ctx context.Context, id string) (redact.Secret, error) {
	plaintext, err := b.open(ctx, id, KindAPIKey)
	if err != nil {
		return redact.Secret{}, err
	}
	return redact.NewSecret(string(plaintext)), nil
}

// ChatGPTTokens returns the stored tokens of a ChatGPT account.
func (b *Book) ChatGPTTokens(ctx context.Context, id string) (ChatGPTTokens, error) {
	plaintext, err := b.open(ctx, id, KindChatGPT)
	if err != nil {
		return ChatGPTTokens{}, err
	}
	return decodeTokens(plaintext)
}

// RefreshChatGPT runs refresh under a lock held per account, so two callers
// never spend the same refresh token, and stores the tokens it returns. The
// client ID of an account never changes. A successful refresh also marks the
// account signed in again.
func (b *Book) RefreshChatGPT(ctx context.Context, id string, refresh Refresher) (ChatGPTTokens, error) {
	defer b.lockAccount(id)()
	current, err := b.ChatGPTTokens(ctx, id)
	if err != nil {
		return ChatGPTTokens{}, err
	}
	next, changed, err := refresh(ctx, current)
	if err != nil {
		return ChatGPTTokens{}, errcode.Wrap(errcode.CodeOf(err), "refresh the tokens failed", err)
	}
	if !changed {
		return current, nil
	}
	if next.ClientID != current.ClientID {
		return ChatGPTTokens{}, errcode.New(errcode.InvalidArgument, "the client id of an account cannot change")
	}
	plaintext, err := encodeTokens(&next)
	if err != nil {
		return ChatGPTTokens{}, err
	}
	sealed, err := b.vault.SealRecord(binding(ProviderOpenAI, KindChatGPT, id), plaintext)
	if err != nil {
		return ChatGPTTokens{}, errcode.Wrap(errcode.CodeOf(err), "seal the tokens failed", err)
	}
	res, err := b.db.ExecContext(ctx, "UPDATE credential_accounts SET secret = ?, signed_in = 1 WHERE id = ?", sealed, id)
	if err = requireRow(res, err, "store the tokens"); err != nil {
		return ChatGPTTokens{}, err
	}
	return next, nil
}

// open reads and opens the secret of an account of the given kind.
func (b *Book) open(ctx context.Context, id string, want Kind) ([]byte, error) {
	var provider, kind string
	var sealed []byte
	err := b.db.QueryRowContext(ctx, "SELECT provider, kind, secret FROM credential_accounts WHERE id = ?", id).Scan(&provider, &kind, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errcode.New(errcode.NotFound, "account not found")
	}
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "read the credential failed", err)
	}
	if Kind(kind) != want {
		return nil, errcode.New(errcode.FailedPrecondition, "the account does not hold that kind of credential")
	}
	plaintext, err := b.vault.OpenRecord(binding(Provider(provider), Kind(kind), id), sealed)
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "open the credential failed", err)
	}
	return plaintext, nil
}

func encodeTokens(t *ChatGPTTokens) ([]byte, error) {
	if t.ClientID == "" || t.Access.IsZero() || t.Refresh.IsZero() {
		return nil, errcode.New(errcode.InvalidArgument, "chatgpt tokens need a client id, an access token and a refresh token")
	}
	record := chatgptRecord{
		ClientID: t.ClientID, Access: t.Access.Reveal(), Refresh: t.Refresh.Reveal(),
		ID: t.ID.Reveal(), ExpiresAt: millis(t.ExpiresAt), Scopes: t.Scopes,
	}
	plaintext, err := json.Marshal(record)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode the tokens failed", err)
	}
	return plaintext, nil
}

func decodeTokens(plaintext []byte) (ChatGPTTokens, error) {
	var record chatgptRecord
	if err := json.Unmarshal(plaintext, &record); err != nil {
		return ChatGPTTokens{}, errcode.New(errcode.Internal, "decode the tokens failed")
	}
	return ChatGPTTokens{
		ClientID: record.ClientID, Access: redact.NewSecret(record.Access), Refresh: redact.NewSecret(record.Refresh),
		ID: redact.NewSecret(record.ID), ExpiresAt: time.UnixMilli(record.ExpiresAt).UTC(), Scopes: record.Scopes,
	}, nil
}
