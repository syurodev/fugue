package hostapi

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

// serviceHandler maps the proto contract to the app backend's domain types.
// It is mounted by Server.Start and satisfies the generated
// hostv1connect.HostServiceHandler interface.
type serviceHandler struct {
	backend app.Backend
}

// clientMessage carries an already-vetted public message to a Connect client.
// err113 forbids dynamic errors.New, and errcode.Expose has already decided
// the message is safe, so the message is a typed string.
type clientMessage string

func (m clientMessage) Error() string { return string(m) }

// genericInternalMessage is all a client learns of a hidden failure.
const genericInternalMessage = "internal error"

// connectCode maps a public errcode code to the Connect transport code. The
// transport has no version code of its own; unsupported_version keeps its
// public code and the host's versions in the typed detail.
func connectCode(c errcode.Code) connect.Code {
	switch c {
	case errcode.InvalidArgument:
		return connect.CodeInvalidArgument
	case errcode.NotFound:
		return connect.CodeNotFound
	case errcode.AlreadyExists:
		return connect.CodeAlreadyExists
	case errcode.FailedPrecondition, errcode.UnsupportedVersion:
		return connect.CodeFailedPrecondition
	case errcode.FrameTooLarge:
		return connect.CodeResourceExhausted
	case errcode.PermissionDenied:
		return connect.CodePermissionDenied
	case errcode.Unavailable:
		return connect.CodeUnavailable
	case errcode.Canceled:
		return connect.CodeCanceled
	case errcode.DeadlineExceeded:
		return connect.CodeDeadlineExceeded
	case errcode.Internal:
		return connect.CodeInternal
	default:
		return connect.CodeInternal
	}
}

// connectError turns err into a Connect error. A public code keeps its
// message and carries the typed ErrorDetail with the public code; hidden
// codes become a generic internal error with no detail (host-api §4).
func (h *serviceHandler) connectError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	code, msg := errcode.Expose(err)
	if !code.IsPublic() {
		return connect.NewError(connect.CodeInternal, clientMessage(genericInternalMessage))
	}
	cerr := connect.NewError(connectCode(code), clientMessage(msg))
	detail := &hostv1.ErrorDetail{Code: string(code)}
	if code == errcode.UnsupportedVersion {
		detail.SupportedApiVersions = h.hostVersions(ctx)
	}
	d, derr := connect.NewErrorDetail(detail)
	if derr != nil {
		// Marshalling a plain message cannot fail; if it ever does, the
		// client sees the generic internal error instead of a wrong reply.
		return connect.NewError(connect.CodeInternal, clientMessage(genericInternalMessage))
	}
	cerr.AddDetail(d)
	return cerr
}

// hostVersions reads the host's supported versions from the backend — the
// runtime is their single source; the server config carries none.
func (h *serviceHandler) hostVersions(ctx context.Context) []string {
	view, err := h.backend.Status(ctx)
	if err != nil {
		return nil
	}
	return view.SupportedAPIVersions
}

// connectionState returns the per-connection state the server attached at
// accept time.
func connectionState(ctx context.Context) (*connState, error) {
	st, ok := ctx.Value(connStateKey{}).(*connState)
	if !ok || st == nil || st.id == "" {
		return nil, ErrConnectionStateMissing
	}
	return st, nil
}

func (h *serviceHandler) Hello(ctx context.Context, req *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	st, serr := connectionState(ctx)
	if serr != nil {
		return nil, h.connectError(ctx, serr)
	}
	// The state machine serializes concurrent hellos on one multiplexed
	// connection: exactly one moves none to acquiring, the rest are rejected.
	if err := st.beginHello(); err != nil {
		return nil, h.connectError(ctx, err)
	}
	kind, err := kindFromProto(req.Msg.ClientKind)
	if err != nil {
		st.failHello()
		return nil, h.connectError(ctx, err)
	}
	res, err := h.backend.Hello(ctx, app.HelloParams{
		Kind:                 kind,
		Name:                 req.Msg.ClientName,
		ClientVersion:        req.Msg.ClientVersion,
		SupportedAPIVersions: req.Msg.SupportedApiVersions,
	})
	if err != nil {
		// A failed hello returns the connection to none: it stays lease-less
		// and the client is free to retry.
		st.failHello()
		return nil, h.connectError(ctx, err)
	}
	// Negotiation first, then the lease: a failed registration returns the
	// connection to none as well.
	if err := h.backend.LeaseAcquired(ctx, st.id, kind); err != nil {
		st.failHello()
		return nil, h.connectError(ctx, err)
	}
	// If the connection closed while the backend calls ran, the close hook
	// has marked it closed without releasing — the hello releases the lease
	// itself. WithoutCancel keeps the release working past the dead request
	// context.
	st.leaseHello(kind, func() { h.backend.LeaseReleased(context.WithoutCancel(ctx), st.id) })
	return connect.NewResponse(&hostv1.HelloResponse{
		ApiVersion:  res.APIVersion,
		HostVersion: res.HostVersion,
	}), nil
}

func (h *serviceHandler) HostStatus(ctx context.Context, _ *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
	if err := requireHelloed(ctx); err != nil {
		return nil, h.connectError(ctx, err)
	}
	view, err := h.backend.Status(ctx)
	if err != nil {
		return nil, h.connectError(ctx, err)
	}
	return connect.NewResponse(&hostv1.HostStatusResponse{
		HostVersion:          view.HostVersion,
		SupportedApiVersions: view.SupportedAPIVersions,
		IncarnationId:        view.Incarnation.IncarnationID,
		StartedAt:            timestamppb.New(view.StartedAt),
		Clients: &hostv1.ClientCounts{
			Tui:           view.Clients.TUI,
			Cli:           view.Clients.CLI,
			App:           view.Clients.App,
			AppBackground: view.Clients.AppBackground,
			Total:         view.Clients.Total,
		},
	}), nil
}

func (h *serviceHandler) Shutdown(ctx context.Context, req *connect.Request[hostv1.ShutdownRequest]) (*connect.Response[hostv1.ShutdownResponse], error) {
	if err := requireHelloed(ctx); err != nil {
		return nil, h.connectError(ctx, err)
	}
	// command_id is mandatory even while draining (host-api §5).
	if req.Msg.CommandId == "" {
		return nil, h.connectError(ctx, errcode.New(errcode.InvalidArgument, "command_id is required"))
	}
	err := h.backend.Shutdown(ctx, app.ShutdownCommand{ID: req.Msg.CommandId, Force: req.Msg.Force})
	if err != nil {
		return nil, h.connectError(ctx, err)
	}
	return connect.NewResponse(&hostv1.ShutdownResponse{}), nil
}

// requireHelloed rejects any rpc that arrives before the connection's hello.
func requireHelloed(ctx context.Context) error {
	st, err := connectionState(ctx)
	if err != nil {
		return err
	}
	if !st.isLeased() {
		return ErrHelloFirst
	}
	return nil
}

// Subscribe streams one topic's events: an optional snapshot (missing or
// unusable cursor), then the events the cursor has not seen yet, in strict
// sequence order (host-api §6). The stream ends with resync_required when the
// client fell too far behind, and cleanly when the host drains.
func (h *serviceHandler) Subscribe(ctx context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	st, serr := connectionState(ctx)
	if serr != nil {
		return h.connectError(ctx, serr)
	}
	// Only a client that completed hello may subscribe; its negotiated kind is
	// the principal the topic authorization decides on.
	if err := requireHelloed(ctx); err != nil {
		return h.connectError(ctx, err)
	}
	var cursor *app.EventCursor
	if c := req.Msg.Cursor; c != nil {
		cursor = &app.EventCursor{Topic: c.Topic, Incarnation: c.IncarnationId, Sequence: c.Sequence}
	}
	sub, err := h.backend.Subscribe(ctx, app.SubscribeParams{
		Principal: string(st.principal()),
		Topic:     req.Msg.Topic,
		Cursor:    cursor,
	})
	if err != nil {
		return h.connectError(ctx, err)
	}
	defer sub.Close()
	// Catch-up from a valid cursor sends no snapshot: the events the client has
	// not seen start immediately (host-api §6).
	if state, seq, ok := sub.Snapshot(); ok {
		serr := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: seq, State: state, IncarnationId: sub.Incarnation()},
		}})
		if serr != nil {
			return errcode.Wrap(errcode.Internal, "send the snapshot", serr)
		}
	}
	for {
		ev, ok := sub.Next(ctx)
		if !ok {
			// The stream ended server-side (drain or resync): the client
			// always gets the end message naming the reason. If the client's
			// own context is gone the send fails and returns.
			serr := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_End{
				End: &hostv1.StreamEnd{ResyncRequired: sub.ResyncRequired()},
			}})
			if serr != nil {
				return errcode.Wrap(errcode.Internal, "send the stream end", serr)
			}
			return nil
		}
		serr := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
			Event: &hostv1.HostEvent{Sequence: ev.Seq, Type: ev.Type, Payload: ev.Payload},
		}})
		if serr != nil {
			return errcode.Wrap(errcode.Internal, "send the event", serr)
		}
	}
}

// kindFromProto maps the proto client kind to the domain kind; an
// unspecified or unknown kind is invalid_argument, never a guess.
func kindFromProto(k hostv1.ClientKind) (app.ClientKind, error) {
	switch k {
	case hostv1.ClientKind_CLIENT_KIND_UNSPECIFIED:
		return "", errcode.New(errcode.InvalidArgument, "client_kind is missing or unknown")
	case hostv1.ClientKind_CLIENT_KIND_TUI:
		return app.KindTUI, nil
	case hostv1.ClientKind_CLIENT_KIND_CLI:
		return app.KindCLI, nil
	case hostv1.ClientKind_CLIENT_KIND_APP:
		return app.KindApp, nil
	case hostv1.ClientKind_CLIENT_KIND_APP_BACKGROUND:
		return app.KindAppBackground, nil
	default:
		return "", errcode.New(errcode.InvalidArgument, "client_kind is missing or unknown")
	}
}

// compile-time check that the handler satisfies the generated interface.
var _ hostv1connect.HostServiceHandler = (*serviceHandler)(nil)
