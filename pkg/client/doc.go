// Package client is the public Go client for the Host API: it finds a running
// host through its discovery file, says Hello with a client kind, and
// subscribes to event topics with catch-up from a cursor (host-api §3, §6,
// §8). It supports the local principal kinds; the per-session token of the
// session principal arrives with the MCP bridge (F9.9).
//
// The client owns exactly one dedicated HTTP/2 connection per Dial: the host's
// lease is per connection (host-api §7), so a Client is that lease. The
// transport never closes the connection as idle; losing it is "disconnected"
// and the caller reconnects by dialing again, which sends a new Hello.
//
// The package has no background goroutines and no clock: every call blocks and
// every stream is driven by the caller, so the caller owns all lifetimes. It
// must not grow an internal dependency, a goroutine or a timer.
//
// Owner: client. Allowed callers: the ui module, cmd/* and tests — never an
// internal/ package.
package client
