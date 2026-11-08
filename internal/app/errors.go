package app

import "github.com/syurodev/fugue/internal/platform/errcode"

// Errors reported by the host runtime. They wrap public errcode codes so the
// transport can show them to a client.
var (
	// ErrNoCommonAPIVersion means the client and the host share no Host API
	// version; the transport attaches the host's supported versions as the
	// typed detail (Q-26 B).
	ErrNoCommonAPIVersion = errcode.New(errcode.UnsupportedVersion, "the client and the host share no Host API version")
	// ErrHostStopping means the host is draining and takes no new clients.
	ErrHostStopping = errcode.New(errcode.Unavailable, "the host is shutting down")
	// ErrSubscriptionDenied means the topic is unknown or not available to the
	// principal: topic authorization defaults to deny (host-api §6, §7), so an
	// unregistered topic and a non-local principal are indistinguishable.
	ErrSubscriptionDenied = errcode.New(errcode.PermissionDenied, "the topic is not available to this principal")
	// ErrEventSeqTooOld means the events after the requested sequence are no
	// longer retained; the hub answers it with a fresh snapshot and the client
	// never sees it, so it is hidden.
	ErrEventSeqTooOld = errcode.New(errcode.Internal, "the requested events are no longer retained")
	// ErrEventTopicInvalid means a topic registration or publish is unusable;
	// a programming error of the topic's owner, never a client's.
	ErrEventTopicInvalid = errcode.New(errcode.Internal, "the event topic is not usable")
	// ErrCursorTopicMismatch means the cursor names a different topic than the
	// subscribe request; honoring it would silently skip events, so it is
	// rejected as malformed.
	ErrCursorTopicMismatch = errcode.New(errcode.InvalidArgument, "the cursor names a different topic")
)
