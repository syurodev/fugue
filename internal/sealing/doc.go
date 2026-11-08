// Package sealing provides one implementation of protected payload sealing and
// session-key lifecycle for the stores, so their formats cannot drift.
// Owner: storage security (G-03). Allowed callers: internal/execstore and
// internal/vendorstore (from F9.3b).
package sealing
