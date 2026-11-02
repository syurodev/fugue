// Package credentials owns the model provider accounts: which providers a person
// signed in to, the credential of each (an API key, or the tokens of Sign in
// with ChatGPT) sealed by the vault, and what the providers' rate limits say
// about each account. PickAccount chooses the account of the next model attempt
// from that state.
//
// The adapter receives a credential explicitly from here and never reads the
// environment. A secret leaves this package only as a redact.Secret, and only
// while the vault is unlocked. An account on a ChatGPT plan never takes part in
// automatic failover (model-adapter M-09).
//
// Owner: model adapter (docs/adr/0001-package-layout.md, G-04). Allowed callers:
// internal/app, which hands the adapters what they need.
package credentials
