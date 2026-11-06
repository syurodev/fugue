// Package hostapi is the Host API transport: it binds the private unix socket,
// trust-checks every directory on the way, checks every connection's peer
// credentials, keeps the host.json discovery file and serves the Connect
// handlers that map proto types to domain types. Owner: Host API. Allowed
// callers: cmd/fugue and this package's tests; it is the only importer of the
// generated protobuf code.
package hostapi
