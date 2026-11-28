package hostapi

import (
	"context"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
)

type memoryHandler struct{ backend app.Backend }

type memoryServiceProvider interface {
	MemoryService() (*app.MemoryService, error)
}

func (h *memoryHandler) service(ctx context.Context) (string, *app.MemoryService, error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "workspace memory")
	if authErr != nil {
		return "", nil, authErr
	}
	provider, ok := h.backend.(memoryServiceProvider)
	if !ok {
		return "", nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the workspace memory service is unavailable"))
	}
	svc, err := provider.MemoryService()
	if err != nil {
		return "", nil, toConnectError(ctx, h.backend, err)
	}
	return principal, svc, nil
}

func (h *memoryHandler) ListEntries(ctx context.Context, req *connect.Request[memoryv1.ListEntriesRequest]) (*connect.Response[memoryv1.ListEntriesResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	kind, err := memoryKindFromProto(req.Msg.GetKind(), true)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	author, err := memoryAuthorFromProto(req.Msg.GetAuthor(), true)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	validity, err := memoryValidityFromProto(req.Msg.GetValidity(), true)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	items, err := svc.ListEntries(ctx, principal, app.MemoryListFilter{
		WorkspaceID: req.Msg.GetWorkspaceId(),
		Kind:        kind,
		Verified:    req.Msg.Verified,
		Author:      author,
		Validity:    validity,
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*memoryv1.MemoryEntry, 0, len(items))
	for i := range items {
		out = append(out, memoryEntryToProto(&items[i]))
	}
	return connect.NewResponse(&memoryv1.ListEntriesResponse{Entries: out}), nil
}

func (h *memoryHandler) SearchEntries(ctx context.Context, req *connect.Request[memoryv1.SearchEntriesRequest]) (*connect.Response[memoryv1.SearchEntriesResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	params, err := memorySearchParamsFromProto(req.Msg)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	resp, err := svc.SearchEntries(ctx, principal, params)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*memoryv1.MemorySearchResult, 0, len(resp.Results))
	for i := range resp.Results {
		out = append(out, memorySearchResultToProto(&resp.Results[i]))
	}
	return connect.NewResponse(&memoryv1.SearchEntriesResponse{
		Results:     out,
		Approximate: resp.Approximate,
	}), nil
}

func (h *memoryHandler) callEntry(
	ctx context.Context,
	fn func(ctx context.Context, principal string, svc *app.MemoryService) (app.MemoryEntryView, error),
) (*memoryv1.MemoryEntry, error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	entry, err := fn(ctx, principal, svc)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return memoryEntryToProto(&entry), nil
}

func callMemoryEntryResponse[Resp any](
	ctx context.Context,
	h *memoryHandler,
	fn func(ctx context.Context, principal string, svc *app.MemoryService) (app.MemoryEntryView, error),
	wrap func(*memoryv1.MemoryEntry) *Resp,
) (*connect.Response[Resp], error) {
	entry, err := h.callEntry(ctx, fn)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(wrap(entry)), nil
}

func (h *memoryHandler) GetEntry(ctx context.Context, req *connect.Request[memoryv1.GetEntryRequest]) (*connect.Response[memoryv1.GetEntryResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	entry, err := svc.GetEntry(ctx, principal, req.Msg.GetWorkspaceId(), req.Msg.GetId(), req.Msg.GetLabel())
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&memoryv1.GetEntryResponse{Entry: memoryEntryToProto(&entry)}), nil
}

func (h *memoryHandler) GetEntryHistory(ctx context.Context, req *connect.Request[memoryv1.GetEntryHistoryRequest]) (*connect.Response[memoryv1.GetEntryHistoryResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	items, err := svc.GetEntryHistory(ctx, principal, req.Msg.GetWorkspaceId(), req.Msg.GetEntryId())
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*memoryv1.MemoryHistoryEntry, 0, len(items))
	for i := range items {
		out = append(out, memoryHistoryEntryToProto(&items[i]))
	}
	return connect.NewResponse(&memoryv1.GetEntryHistoryResponse{History: out}), nil
}

func (h *memoryHandler) WriteEntry(ctx context.Context, req *connect.Request[memoryv1.WriteEntryRequest]) (*connect.Response[memoryv1.WriteEntryResponse], error) {
	return callMemoryEntryResponse(ctx, h, func(callCtx context.Context, principal string, svc *app.MemoryService) (app.MemoryEntryView, error) {
		cmd, err := memoryWriteCmdFromProto(req.Msg)
		if err != nil {
			return app.MemoryEntryView{}, err
		}
		return svc.WriteEntry(callCtx, principal, cmd)
	}, func(entry *memoryv1.MemoryEntry) *memoryv1.WriteEntryResponse {
		return &memoryv1.WriteEntryResponse{Entry: entry}
	})
}

func (h *memoryHandler) VerifyEntry(ctx context.Context, req *connect.Request[memoryv1.VerifyEntryRequest]) (*connect.Response[memoryv1.VerifyEntryResponse], error) {
	return callMemoryEntryResponse(ctx, h, func(callCtx context.Context, principal string, svc *app.MemoryService) (app.MemoryEntryView, error) {
		return svc.VerifyEntry(callCtx, principal, req.Msg.GetWorkspaceId(), req.Msg.GetId(), req.Msg.GetExpectedRevision())
	}, func(entry *memoryv1.MemoryEntry) *memoryv1.VerifyEntryResponse {
		return &memoryv1.VerifyEntryResponse{Entry: entry}
	})
}

func (h *memoryHandler) RestoreEntry(ctx context.Context, req *connect.Request[memoryv1.RestoreEntryRequest]) (*connect.Response[memoryv1.RestoreEntryResponse], error) {
	return callMemoryEntryResponse(ctx, h, func(callCtx context.Context, principal string, svc *app.MemoryService) (app.MemoryEntryView, error) {
		return svc.RestoreEntry(callCtx, principal, req.Msg.GetWorkspaceId(), req.Msg.GetId(), req.Msg.GetRestoreRevision(), req.Msg.GetExpectedRevision())
	}, func(entry *memoryv1.MemoryEntry) *memoryv1.RestoreEntryResponse {
		return &memoryv1.RestoreEntryResponse{Entry: entry}
	})
}

var _ memoryv1connect.MemoryServiceHandler = (*memoryHandler)(nil)
