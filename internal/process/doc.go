// Package process manages supervised OS process trees for vendor CLI sessions.
//
// On Linux, sessions run inside a user systemd scope (`systemd-run --user --scope`,
// cgroup v2) and Stop returns StopProofProved only when the cgroup is verified
// empty. On macOS and when a user systemd scope is unavailable or unverified,
// processes run in a process group with periodic descendant PID tracking and
// Stop returns StopProofUnknown with a fixed Reason code. Windows returns a
// fail-closed stub.
//
// The vendor-runtime §13 defaults this package owns, each enforced at one site:
//
//   - MaxFrameSize (reader.go, 8 MiB): a larger stdout JSON line fails the
//     reader with ErrFrameTooLarge (public code frame_too_large).
//   - DefaultStderrRingCapacity (ring.go, 64 KiB): stderr keeps its newest
//     64 KiB tail and never errors; older bytes are dropped by design.
//   - interruptWaitTimeout, sigintWaitTimeout, sigtermWaitTimeout (stop.go,
//     10 s / 5 s / 5 s): the stop order escalates through them and reports
//     StopProofUnknown when a step expires; a caller whose Close deadline
//     expires first sees deadline_exceeded while the stop order still runs.
//
// The remaining §13 defaults live with their owners: the 30 s handshake is
// vendoradapter/claude.DefaultHandshakeTimeout (fails typed with
// deadline_exceeded), and the per-host concurrent-session cap is the
// scheduler-owned value in internal/app (vendor.capacity).
//
// Owner: vendor runtime (VR-09).
// Allowed callers: internal/vendor and its adapter packages (claude, codex, agy).
package process
