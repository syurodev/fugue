// Package secrets holds the Secret Store metadata model and off-thread
// Host API worker for F10.15b (desktop-app §5.6).
//
// The package stores only unsealed metadata (secret_id, scope, name,
// description, timestamps); plaintext secret bytes enter only transiently in
// PutSecret requests and are zeroed immediately after sending.
package secrets
