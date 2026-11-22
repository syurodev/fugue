package hostapi

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	secretsv1 "github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/secrets/v1/secretsv1connect"
)

const secretsRequestMaxBytes = 128 * 1024

type secretsHandler struct{ backend app.Backend }

type secretServiceProvider interface {
	SecretService() (*app.SecretService, error)
}

func (h *secretsHandler) service(ctx context.Context) (string, *app.SecretService, error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "secret store")
	if authErr != nil {
		return "", nil, authErr
	}
	provider, ok := h.backend.(secretServiceProvider)
	if !ok {
		return "", nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the secret service is unavailable"))
	}
	svc, err := provider.SecretService()
	if err != nil {
		return "", nil, toConnectError(ctx, h.backend, err)
	}
	return principal, svc, nil
}

func (h *secretsHandler) ListSecrets(ctx context.Context, req *connect.Request[secretsv1.ListSecretsRequest]) (*connect.Response[secretsv1.ListSecretsResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	scopeKind, err := scopeKindFromProto(req.Msg.GetScopeKind(), true)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	items, err := svc.ListSecrets(ctx, principal, scopeKind, req.Msg.GetScopeId())
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*secretsv1.SecretMeta, 0, len(items))
	for i := range items {
		out = append(out, protoSecretMeta(&items[i]))
	}
	return connect.NewResponse(&secretsv1.ListSecretsResponse{Secrets: out}), nil
}

func (h *secretsHandler) PutSecret(ctx context.Context, req *connect.Request[secretsv1.PutSecretRequest]) (*connect.Response[secretsv1.PutSecretResponse], error) {
	if req.Msg != nil {
		defer clear(req.Msg.Value)
	}
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	scopeKind, err := scopeKindFromProto(req.Msg.GetScopeKind(), false)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	meta, err := svc.PutSecret(ctx, &app.PutSecretCommand{
		Principal:   principal,
		CommandID:   req.Msg.GetCommandId(),
		ScopeKind:   scopeKind,
		ScopeID:     req.Msg.GetScopeId(),
		Name:        req.Msg.GetName(),
		Description: req.Msg.GetDescription(),
		Value:       req.Msg.GetValue(),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&secretsv1.PutSecretResponse{Secret: protoSecretMeta(&meta)}), nil
}

func (h *secretsHandler) DeleteSecret(ctx context.Context, req *connect.Request[secretsv1.DeleteSecretRequest]) (*connect.Response[secretsv1.DeleteSecretResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	deleted, err := svc.DeleteSecret(ctx, &app.DeleteSecretCommand{
		Principal: principal,
		CommandID: req.Msg.GetCommandId(),
		SecretID:  req.Msg.GetSecretId(),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&secretsv1.DeleteSecretResponse{Deleted: deleted}), nil
}

func scopeKindFromProto(k secretsv1.SecretScopeKind, allowEmpty bool) (string, error) {
	switch k {
	case secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_UNSPECIFIED:
		if allowEmpty {
			return "", nil
		}
		return "", errcode.New(errcode.InvalidArgument, "scope_kind is required")
	case secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL:
		return app.SecretScopeGlobal, nil
	case secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_WORKSPACE:
		return app.SecretScopeWorkspace, nil
	default:
		return "", errcode.New(errcode.InvalidArgument, "unknown secret scope_kind")
	}
}

func scopeKindToProto(k string) secretsv1.SecretScopeKind {
	switch k {
	case app.SecretScopeGlobal:
		return secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_GLOBAL
	case app.SecretScopeWorkspace:
		return secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_WORKSPACE
	default:
		return secretsv1.SecretScopeKind_SECRET_SCOPE_KIND_UNSPECIFIED
	}
}

func protoSecretMeta(m *app.SecretMetaView) *secretsv1.SecretMeta {
	return &secretsv1.SecretMeta{
		SecretId:    m.SecretID,
		ScopeKind:   scopeKindToProto(m.ScopeKind),
		ScopeId:     m.ScopeID,
		Name:        m.Name,
		Description: m.Description,
		CreatedAt:   timestamppb.New(m.CreatedAt),
		UpdatedAt:   timestamppb.New(m.UpdatedAt),
	}
}

var _ secretsv1connect.SecretServiceHandler = (*secretsHandler)(nil)
