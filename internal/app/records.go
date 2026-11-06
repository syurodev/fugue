package app

import (
	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// The host records are the small canonical facts the kernel requires as
// evidence references but cannot invent: what admitted the run, what
// authorized a dispatch, what a step's context is. Each record is sealed as a
// payload in the same append as the first event naming its ID, so every
// reference the kernel stores has real bytes behind it. Their plaintext is a
// pure function of the supplied inputs, which keeps equal facts byte-equal and
// lets the receipt dedup compare them.

// Payload kinds of the records and of the event payloads the executors seal.
// The execution store accepts only lowercase and underscore kinds.
const (
	kindQueueItem          = "queue_item"
	kindQueueDecision      = "queue_decision"
	kindModelAuthorization = "model_authorization"
	kindToolAuthorization  = "tool_authorization"
	kindContext            = "context"
)

// Values the slice fixes by design; none of them is a decision the kernel
// would otherwise make (queue policy, provider, account, approval class).
const (
	queuePolicyAdmitWhenIdle = "admit_when_idle"
	recordFormatVersion      = "fugue-trace-a-v1"
	providerFaux             = "faux"
	accountNone              = "none"
	approvalNone             = "none"
	effectClassPureRead      = "pure_read"
	toolPureRead             = "pure_read"
)

type queueItemRecord struct {
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
}

type queueDecisionRecord struct {
	Policy  string `json:"policy"`
	Version string `json:"version"`
	ItemRef string `json:"item_ref"`
}

type modelAuthorizationRecord struct {
	ModelRef         string `json:"model_ref"`
	Provider         string `json:"provider"`
	Account          string `json:"account"`
	ProviderSettings string `json:"provider_settings"`
	CatalogVersion   string `json:"catalog_version"`
}

type toolAuthorizationRecord struct {
	Tool              string `json:"tool"`
	DescriptorVersion string `json:"descriptor_version"`
	EffectClass       string `json:"effect_class"`
	Approval          string `json:"approval"`
}

// contextItem is one transcript entry of a step's context: where it came from
// and its content. Content stays bytes so the context engine (F4.5) can replace
// the representation without a format change.
type contextItem struct {
	Role    string `json:"role"`
	Ref     string `json:"ref"`
	Content []byte `json:"content"`
}

func sealRecord(gen ids.Generator, kind string, record any) (coordinator.Payload, error) {
	id, err := gen.NewID()
	if err != nil {
		return coordinator.Payload{}, errcode.New(errcode.Internal, "new record id")
	}
	plaintext, err := codec.MarshalCanonical(record)
	if err != nil {
		return coordinator.Payload{}, errcode.New(errcode.Internal, "marshal record")
	}
	return coordinator.Payload{ID: id, Kind: kind, Plaintext: plaintext}, nil
}

// newQueueItem records the session's queue entry a run is accepted with.
func newQueueItem(gen ids.Generator, sessionID, runID string) (coordinator.Payload, error) {
	return sealRecord(gen, kindQueueItem, queueItemRecord{SessionID: sessionID, RunID: runID})
}

// newQueueDecision records why the host admitted a run now: the queue policy
// and the queue item it decided on.
func newQueueDecision(gen ids.Generator, itemRef string) (coordinator.Payload, error) {
	return sealRecord(gen, kindQueueDecision, queueDecisionRecord{
		Policy:  queuePolicyAdmitWhenIdle,
		Version: recordFormatVersion,
		ItemRef: itemRef,
	})
}

// newModelAuthorization records the host's dispatch authorization of a model
// attempt. Trace A has no credentials, so the record names no account; its ID
// backs DispatchAuthorizationRef, ProviderSettingsRef and AccountID of the
// dispatch facts.
func newModelAuthorization(gen ids.Generator, modelRef string) (coordinator.Payload, error) {
	return sealRecord(gen, kindModelAuthorization, modelAuthorizationRecord{
		ModelRef:         modelRef,
		Provider:         providerFaux,
		Account:          accountNone,
		ProviderSettings: recordFormatVersion,
		CatalogVersion:   recordFormatVersion,
	})
}

// newToolAuthorization records the host's dispatch authorization of a tool
// attempt: the tool, its descriptor version and the effect class, with the
// approval the class requires (none for pure_read).
func newToolAuthorization(gen ids.Generator, descriptorVersion string) (coordinator.Payload, error) {
	return sealRecord(gen, kindToolAuthorization, toolAuthorizationRecord{
		Tool:              toolPureRead,
		DescriptorVersion: descriptorVersion,
		EffectClass:       effectClassPureRead,
		Approval:          approvalNone,
	})
}

// newContext seals a step's context: the transcript material the model executor
// turns into a provider request. The context engine (F4.5) replaces this
// assembly; no summarization, pruning or layering happens here.
func newContext(gen ids.Generator, items []contextItem) (coordinator.Payload, error) {
	return sealRecord(gen, kindContext, items)
}

// decodeContext reads back a context payload the way it was written.
func decodeContext(plaintext []byte) ([]contextItem, error) {
	var items []contextItem
	if err := codec.UnmarshalCanonical(plaintext, &items); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, "decode context")
	}
	return items, nil
}
