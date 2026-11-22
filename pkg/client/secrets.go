package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"connectrpc.com/connect"

	secretsv1 "github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1/secretsv1connect"
)

// TopicSecrets and its event names mirror the host's durable secrets topic.
// Events carry metadata only, never secret values.
const (
	TopicSecrets       = "secrets"
	EventSecretPut     = "secret_put"
	EventSecretDeleted = "secret_deleted"

	SecretScopeGlobal    = "global"
	SecretScopeWorkspace = "workspace"
)

// SecretMeta is the unsealed metadata of one stored secret.
type SecretMeta struct {
	SecretID    string    `json:"secret_id"`
	ScopeKind   string    `json:"scope_kind"`
	ScopeID     string    `json:"scope_id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PutSecretInput carries one PutSecret request. Value is cleared after sending.
type PutSecretInput struct {
	CommandID   string
	ScopeKind   string
	ScopeID     string
	Name        string
	Description string
	Value       []byte
}

// SecretTopicEvent is the payload of a `secrets` topic `secret_put` or `secret_deleted` event.
type SecretTopicEvent struct {
	SecretID  string `json:"secret_id"`
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id,omitempty"`
	Name      string `json:"name"`
}

// SecretsSnapshot is the `secrets` topic snapshot payload.
type SecretsSnapshot struct {
	Secrets []SecretMeta `json:"secrets"`
}

// ListSecrets returns unsealed secret metadata matching the optional scope filter.
func (c *Client) ListSecrets(ctx context.Context, scopeKind, scopeID string) ([]SecretMeta, error) {
	res, err := secretsv1connect.NewSecretServiceClient(c.hc, c.base).ListSecrets(ctx,
		connect.NewRequest(&secretsv1.ListSecretsRequest{
			ScopeKind: secretScopeKindToProto(scopeKind),
			ScopeId:   scopeID,
		}))
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	out := make([]SecretMeta, 0, len(res.Msg.GetSecrets()))
	for _, s := range res.Msg.GetSecrets() {
		out = append(out, secretMetaFromProto(s))
	}
	return out, nil
}

var errPutInputRequired = errors.New("input is required")

// PutSecret stores or updates a secret and clears in.Value immediately after sending.
func (c *Client) PutSecret(ctx context.Context, in *PutSecretInput) (SecretMeta, error) {
	if in == nil {
		return SecretMeta{}, fmt.Errorf("put secret: %w", errPutInputRequired)
	}
	defer clear(in.Value)
	req := &secretsv1.PutSecretRequest{
		CommandId:   in.CommandID,
		ScopeKind:   secretScopeKindToProto(in.ScopeKind),
		ScopeId:     in.ScopeID,
		Name:        in.Name,
		Description: in.Description,
		Value:       slices.Clone(in.Value),
	}
	defer clear(req.Value)
	res, err := secretsv1connect.NewSecretServiceClient(c.hc, c.base).PutSecret(ctx, connect.NewRequest(req))
	if err != nil {
		return SecretMeta{}, fmt.Errorf("put secret: %w", err)
	}
	return secretMetaFromProto(res.Msg.GetSecret()), nil
}

// DeleteSecret deletes a secret by secret_id.
func (c *Client) DeleteSecret(ctx context.Context, commandID, secretID string) (bool, error) {
	res, err := secretsv1connect.NewSecretServiceClient(c.hc, c.base).DeleteSecret(ctx,
		connect.NewRequest(&secretsv1.DeleteSecretRequest{
			CommandId: commandID,
			SecretId:  secretID,
		}))
	if err != nil {
		return false, fmt.Errorf("delete secret: %w", err)
	}
	return res.Msg.GetDeleted(), nil
}

// DecodeSecretsSnapshot decodes the `secrets` topic snapshot payload.
func DecodeSecretsSnapshot(payload []byte) (SecretsSnapshot, error) {
	var out SecretsSnapshot
	if err := json.Unmarshal(payload, &out); err != nil {
		return SecretsSnapshot{}, fmt.Errorf("decode secrets snapshot: %w", err)
	}
	return out, nil
}

// DecodeSecretEvent decodes a `secrets` topic `secret_put` or `secret_deleted` event payload.
func DecodeSecretEvent(payload []byte) (SecretTopicEvent, error) {
	var out SecretTopicEvent
	if err := json.Unmarshal(payload, &out); err != nil {
		return SecretTopicEvent{}, fmt.Errorf("decode secret event: %w", err)
	}
	return out, nil
}

func secretScopeKindToProto(k string) secretsv1.SecretScopeKind {
	switch k {
	case SecretScopeGlobal:
		return secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL
	case SecretScopeWorkspace:
		return secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_WORKSPACE
	default:
		return secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_UNSPECIFIED
	}
}

func secretScopeKindFromProto(k secretsv1.SecretScopeKind) string {
	switch k {
	case secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL:
		return SecretScopeGlobal
	case secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_WORKSPACE:
		return SecretScopeWorkspace
	case secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

func secretMetaFromProto(s *secretsv1.SecretMeta) SecretMeta {
	if s == nil {
		return SecretMeta{}
	}
	var created, updated time.Time
	if s.GetCreatedAt() != nil {
		created = s.GetCreatedAt().AsTime()
	}
	if s.GetUpdatedAt() != nil {
		updated = s.GetUpdatedAt().AsTime()
	}
	return SecretMeta{
		SecretID:    s.GetSecretId(),
		ScopeKind:   secretScopeKindFromProto(s.GetScopeKind()),
		ScopeID:     s.GetScopeId(),
		Name:        s.GetName(),
		Description: s.GetDescription(),
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}
