// Package hostconn owns the app's connection to the host: it finds the host
// through its discovery file, holds one leased client connection, keeps one
// event stream per topic, and reconnects with a bounded backoff when the
// connection drops (desktop-app §4). Deliveries and connection-state changes
// reach the window goroutine over one channel; the package never touches UI
// state itself.
//
// Time comes only from the injected Clock, so the backoff is testable; every
// goroutine here is owned by the Manager and ends with Stop or the window
// context. pkg/client does the blocking work; this package only moves its
// results.
//
// Owner: desktop app (ui module). Allowed callers: ui/cmd/fugue-app and ui
// internal packages; the core module never imports this module.
package hostconn
