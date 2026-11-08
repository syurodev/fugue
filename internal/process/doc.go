// Package process manages supervised OS process trees for vendor CLI sessions.
//
// On Linux, sessions run inside a user systemd scope (`systemd-run --user --scope`,
// cgroup v2) and Stop returns StopProofProved only when the cgroup is verified
// empty. On macOS and when a user systemd scope is unavailable or unverified,
// processes run in a process group with periodic descendant PID tracking and
// Stop returns StopProofUnknown with a fixed Reason code. Windows returns a
// fail-closed stub.
//
// Owner: vendor runtime (VR-09).
// Allowed callers: internal/vendor and its adapter packages (claude, codex, agy).
package process
