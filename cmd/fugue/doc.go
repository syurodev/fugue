// Command fugue is the Fugue binary. At F5.1 it runs one subcommand, serve:
// the foreground host that owns the data directory and serves the Host API
// over its private socket. Further subcommands arrive with the client (F7.1).
//
// Owner: Host API (docs/roadmap.md F5.1). Allowed callers: the user, and the
// F7.1 client auto-start, which runs it detached with stdout and stderr in
// the data directory. Nothing in the module imports it. main.go holds the
// printing, the process context and the environment load; RunServe is the
// testable entry point and exits through its return code.
package main
