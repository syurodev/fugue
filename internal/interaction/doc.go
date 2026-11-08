// Package interaction owns the in-memory approval and selection requests for
// the current host incarnation, the session-scoped confirmation grants and the
// one-use receipts claimed at the tool execution boundary.
//
// Requests are keyed by a neutral operation ID so a retried check reads back
// the same request without asking twice, and every request may be answered at
// most once. Two key formats exist (interaction §4.3 and the v0.14 scope
// note); callers never hand-build keys:
//
//   - NativeOperationKey keys a native tool check on
//     (invocationID, attemptID, checkGeneration) and formats as
//     "native:<invocationID>:<attemptID>:<checkGeneration>".
//   - VendorOperationKey keys a relayed vendor request on
//     (sessionID, turnID, vendorRequestID) and formats as
//     "vendor:<sessionID>:<turnID>:<vendorRequestID>".
//
// Process restart or vault lock invalidates all pending requests, session
// grants and unconsumed receipts. A request with no live Responder waits
// until a client picks it up (I-07); only a headless host resolves requests
// as unavailable. Relayed vendor requests (vendor-runtime VR-05) carry the
// vendor's own options verbatim; the owner never creates a grant or an
// execution receipt for them and the answer is delivered back to the caller
// exactly once.
//
// Owner: interaction (docs/adr/0001-package-layout.md, G-05). Allowed callers:
// internal/tools and internal/app.
package interaction
