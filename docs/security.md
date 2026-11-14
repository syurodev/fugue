# Security notes

Accepted risks and trust boundaries of the running host. The design pages in base-doc decide
the model; this page records what the implementation relies on and what it does not cover.
An item here is a known limit, not a bug to fix without a design change.

## 1. Host API principals

| Principal | How it is established | What it may do |
| --- | --- | --- |
| `local` | A connection on the private socket whose peer UID is the host's user, after `Hello` | Every RPC: answer approvals, vendor sessions, workspaces, shutdown |
| `session` | A connection that calls `fugue.sessiontools.v1` `Attach` with a live session token | `ListTools` and `CallTool` of that session and role; every other RPC is denied typed |

- The UID check proves "the same OS user", not "the Fugue app". A process of the same user can
  already read the data directory, so this is the accepted model (`host-api §7`).
- A connection is one principal for its whole life. A session connection cannot say `Hello`,
  and a connection that said `Hello` cannot attach a token.

## 2. Session tokens

- 256 bits from the host's randomness port, one per spawned vendor session, bound to the
  session ID and role.
- The host keeps only the SHA-256 hash, in memory. The clear token exists in the environment of
  the vendor process (`FUGUE_SESSION_TOKEN`), which `fugue mcp-bridge` inherits. It is never
  in argv, a file, a log line, an error or an audit record.
- Revoked when the session's actor stops (close, process exit, failure), when the session is
  purged, when a spawn fails, and when the host stops. Revocation also ends every connection
  that attached with the token.
- The token is readable by any process of the same user that can read the vendor process's
  environment. It opens only Fugue's own tools of that session, and each call still passes the
  interaction policy (`vendor-runtime` VR-13), so this matches the `local` trust model above.

## 3. Vendor processes and the local principal (Q-41 A)

A vendor CLI runs as the host's user, so without a further rule it could connect to the
socket, say it is the desktop app and answer its own approval requests. The host therefore
records the peer PID when it accepts a connection (`SO_PEERCRED` on Linux, `LOCAL_PEERPID` on
macOS) and refuses `Hello` when that process belongs to a live vendor session:

- Linux with a systemd user scope: the PID is listed in the session scope's `cgroup.procs`.
- macOS, and Linux without a scope: the PID is in the session's descendant tracker, after a
  fresh scan of the process table that follows parent links and the leader's process group.

The check fails closed: an unknown peer PID, an unreadable scope or process table, and a PID
missing from the table all deny the local principal. The `session` principal is not affected.

**Residual risk.** Where membership comes from the process table (macOS, and Linux without a
systemd user scope), a vendor descendant that leaves the session's process group and loses
its parent (for example a double fork with `setsid`) between two tracker scans (100 ms) is
re-parented to the init process before the host has seen it under a tracked parent. The host
no longer recognizes it as a vendor process and it can become a local principal. Linux with a
scope has no such gap. Closing it on macOS needs kernel-level containment and is out of
stage A. Related limits of the same mechanism:

- The check runs at `Hello`. A local client that was accepted keeps its principal.
- It covers live sessions. Processes a closed session left behind (stop proof `unknown`) are
  not tracked any more.
