package hostapi

import (
	"context"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
)

// vendorHandler maps fugue.vendors.v1 onto the app vendor service. The
// principal comes from the connection's hello, never from a request field;
// the service checks it again.
type vendorHandler struct{ backend app.Backend }

type vendorProvider interface {
	VendorService() (*app.VendorService, error)
}

func (h *vendorHandler) auth(ctx context.Context) (*app.VendorService, string, error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "vendor sessions")
	if authErr != nil {
		return nil, "", authErr
	}
	provider, ok := h.backend.(vendorProvider)
	if !ok {
		return nil, "", toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the vendor service is unavailable"))
	}
	svc, err := provider.VendorService()
	if err != nil {
		return nil, "", toConnectError(ctx, h.backend, err)
	}
	return svc, principal, nil
}

func (h *vendorHandler) ProbeVendors(ctx context.Context, req *connect.Request[vendorsv1.ProbeVendorsRequest]) (*connect.Response[vendorsv1.ProbeVendorsResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(req.Msg.Vendors))
	for _, v := range req.Msg.Vendors {
		names = append(names, vendorName(v))
	}
	probes, err := svc.Probe(ctx, principal, names)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := &vendorsv1.ProbeVendorsResponse{}
	for i := range probes {
		p := &probes[i]
		c := p.Capability
		out.Probes = append(out.Probes, &vendorsv1.VendorProbe{
			Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, Version: p.Version, VersionTested: p.VersionTested,
			VersionWarning: p.VersionWarning, Login: p.Login,
			Capability: &vendorsv1.Capability{
				PerRequestApproval: c.PerRequestApproval, BlockAllToolCalls: c.BlockAllToolCalls, Interrupt: c.Interrupt,
				Steer: c.Steer, Resume: c.Resume, PartialDeltas: c.PartialDeltas, VendorSlashCommands: c.VendorSlashCommands,
				DirectQuota: c.DirectQuota, ModelList: c.ModelList,
			},
		})
	}
	return connect.NewResponse(out), nil
}

func (h *vendorHandler) StartSession(ctx context.Context, req *connect.Request[vendorsv1.StartSessionRequest]) (*connect.Response[vendorsv1.StartSessionResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	view, err := svc.Start(ctx, principal, &app.StartVendorSession{
		CommandID: m.CommandId, Vendor: vendorName(m.Vendor), WorkspaceID: m.WorkspaceId, RootName: m.RootName, Profile: vendorProfile(m.Profile),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&vendorsv1.StartSessionResponse{Session: protoVendorSession(&view)}), nil
}

func (h *vendorHandler) Send(ctx context.Context, req *connect.Request[vendorsv1.SendRequest]) (*connect.Response[vendorsv1.SendResponse], error) {
	if err := h.input(ctx, false, app.VendorInput{CommandID: req.Msg.CommandId, SessionID: req.Msg.SessionId, Text: req.Msg.Text}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&vendorsv1.SendResponse{}), nil
}

func (h *vendorHandler) Steer(ctx context.Context, req *connect.Request[vendorsv1.SteerRequest]) (*connect.Response[vendorsv1.SteerResponse], error) {
	if err := h.input(ctx, true, app.VendorInput{CommandID: req.Msg.CommandId, SessionID: req.Msg.SessionId, Text: req.Msg.Text}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&vendorsv1.SteerResponse{}), nil
}

func (h *vendorHandler) input(ctx context.Context, steer bool, cmd app.VendorInput) error {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return err
	}
	deliver := svc.Send
	if steer {
		deliver = svc.Steer
	}
	if deliverErr := deliver(ctx, principal, cmd); deliverErr != nil {
		return toConnectError(ctx, h.backend, deliverErr)
	}
	return nil
}

func (h *vendorHandler) Interrupt(ctx context.Context, req *connect.Request[vendorsv1.InterruptRequest]) (*connect.Response[vendorsv1.InterruptResponse], error) {
	status, err := h.stop(ctx, false, app.VendorStop{CommandID: req.Msg.CommandId, SessionID: req.Msg.SessionId})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&vendorsv1.InterruptResponse{Status: status}), nil
}

func (h *vendorHandler) CloseSession(ctx context.Context, req *connect.Request[vendorsv1.CloseSessionRequest]) (*connect.Response[vendorsv1.CloseSessionResponse], error) {
	status, err := h.stop(ctx, true, app.VendorStop{CommandID: req.Msg.CommandId, SessionID: req.Msg.SessionId})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&vendorsv1.CloseSessionResponse{Status: status}), nil
}

func (h *vendorHandler) stop(ctx context.Context, closeSession bool, cmd app.VendorStop) (vendorsv1.OperationStatus, error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return vendorsv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED, err
	}
	stop := svc.Interrupt
	if closeSession {
		stop = svc.Close
	}
	done, err := stop(ctx, principal, cmd)
	if err != nil {
		return vendorsv1.OperationStatus_OPERATION_STATUS_UNSPECIFIED, toConnectError(ctx, h.backend, err)
	}
	if done {
		return vendorsv1.OperationStatus_OPERATION_STATUS_DONE, nil
	}
	return vendorsv1.OperationStatus_OPERATION_STATUS_PENDING, nil
}

func (h *vendorHandler) ResumeSession(ctx context.Context, req *connect.Request[vendorsv1.ResumeSessionRequest]) (*connect.Response[vendorsv1.ResumeSessionResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	view, err := svc.Resume(ctx, principal, &app.ResumeVendorSession{
		CommandID: m.CommandId, SessionID: m.SessionId, WorkspaceID: m.WorkspaceId, RootName: m.RootName, Profile: vendorProfile(m.Profile),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&vendorsv1.ResumeSessionResponse{Session: protoVendorSession(&view)}), nil
}

func (h *vendorHandler) ListSessions(ctx context.Context, _ *connect.Request[vendorsv1.ListSessionsRequest]) (*connect.Response[vendorsv1.ListSessionsResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	views, seq, err := svc.List(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := &vendorsv1.ListSessionsResponse{Sequence: seq}
	for i := range views {
		out.Sessions = append(out.Sessions, protoVendorSession(&views[i]))
	}
	return connect.NewResponse(out), nil
}

func (h *vendorHandler) ListCommands(ctx context.Context, req *connect.Request[vendorsv1.ListCommandsRequest]) (*connect.Response[vendorsv1.ListCommandsResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	groups, revision, err := svc.Commands(ctx, principal, req.Msg.SessionId)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	response := &vendorsv1.ListCommandsResponse{CommandsRevision: revision}
	for i := range groups {
		group := &vendorsv1.CommandGroup{Name: groups[i].Name}
		for _, name := range groups[i].VendorCommands {
			group.VendorCommands = append(group.VendorCommands, &vendorsv1.VendorCommand{Name: name})
		}
		for _, command := range groups[i].FugueCommands {
			group.FugueCommands = append(group.FugueCommands, &vendorsv1.FugueCommand{
				Name: command.Name, Owner: command.Owner, Available: command.Available,
			})
		}
		response.Groups = append(response.Groups, group)
	}
	return connect.NewResponse(response), nil
}

// vendorName maps the enum to the app's vendor name; an unspecified value
// maps to the empty name, which the service rejects as unsupported.
func vendorName(v vendorsv1.Vendor) string {
	switch v {
	case vendorsv1.Vendor_VENDOR_CLAUDE:
		return app.VendorClaude
	case vendorsv1.Vendor_VENDOR_CODEX:
		return "codex"
	case vendorsv1.Vendor_VENDOR_AGY:
		return "agy"
	case vendorsv1.Vendor_VENDOR_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

func vendorProfile(p *vendorsv1.SessionProfile) app.VendorProfile {
	if p == nil {
		return app.VendorProfile{}
	}
	return app.VendorProfile{Model: p.Model, Effort: p.Effort, ResumeSessionID: p.ResumeSessionId}
}

func protoVendorSession(v *app.VendorSessionView) *vendorsv1.VendorSession {
	vendor := vendorsv1.Vendor_VENDOR_UNSPECIFIED
	if v.Vendor == app.VendorClaude {
		vendor = vendorsv1.Vendor_VENDOR_CLAUDE
	}
	return &vendorsv1.VendorSession{
		SessionId: v.ID, Vendor: vendor, State: v.State, WaitingReason: v.WaitingReason,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

var _ vendorsv1connect.VendorServiceHandler = (*vendorHandler)(nil)
