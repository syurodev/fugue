package app

// TopicInteraction is the ephemeral topic carrying interaction request lifecycle
// events: new requests and resolved decisions (host-api §6, interaction §8).
const TopicInteraction = "interaction"

// Event types published on TopicInteraction.
const (
	EventInteractionRequested = "requested"
	EventInteractionResolved  = "resolved"
)

// resolvedPayload is the payload of an EventInteractionResolved event. It
// contains only metadata and outcome: no summary, prompt, answer or note
// content is ever included (interaction §7, §8, I-E08).
type resolvedPayload struct {
	RequestID string `json:"request_id"`
	Outcome   string `json:"outcome"`
}
