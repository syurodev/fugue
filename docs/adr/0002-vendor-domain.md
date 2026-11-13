# ADR 0002: Package layout of the vendor domain

- **Status:** Accepted by the owner, 2026-11-07
- **Date:** 2026-11-07
- **Roadmap item:** F9.1 (this repository's stage A vendor architecture gate)
- **Design:** `vendor-runtime §6, §8, §10, §12`, `transcript §2, §6`, `orchestration §3, §6.6, §9`,
  `desktop-app §4`, `host-api HA-06, §5.1, §7`, `architecture` (v0.14 paragraph).

## Context

Under activation v0.14 (base-doc PR #8, 2026-11-07), stage A pivots from native harness
development to running first-party vendor CLIs (Claude Code, Codex, Antigravity) under the user's
own subscription logins. Fugue maintains ownership of the vault, storage, interaction policy,
scheduler, task lifecycle, and desktop user interface, while delegating the model and tool execution
loop to the respective vendor CLI.

As settled in `architecture` (v0.14) and `vendor-runtime` VR-01, vendor sessions constitute an
independent domain (`internal/vendor`, `internal/vendorstore`), rather than simulated native runs.
They share the single host process, Host API socket, SQLite database, cryptographic vault,
interaction policy engine, and machine scheduler, but do not execute through the native kernel
(`internal/kernel`) or coordinator (`internal/coordinator`).

ADR 0001 established the 7-layer architecture and package boundaries for the native harness. This
ADR establishes the package layout, layers, import edges, database table ownership, public client SDK,
UI module boundaries, and CLI subcommands for stage A (vendor runtime, Gio desktop app, and team
orchestration in roadmap items F9–F11).

The tables below are **machine-read** by `internal/arch`. Column ordering and back-ticked package
and table names follow the established format of ADR 0001.

## Decision

### 1. Layers

The vendor domain packages integrate into the established 7-layer hierarchy:

| Layer | Name | Rule |
| --- | --- | --- |
| 0 | platform | Stdlib only. Injected ports for clock, IDs, supervision, error codes, and redaction. |
| 1 | foundation | Logic or narrow OS concerns: `internal/process` (process tree supervision) and `internal/gitops` (git worktree manager) join `config`, `crypto`, `storage`, `interaction`, `sandbox`, `snapshot`. |
| 2 | secured stores | Built on layer 1: `codec`, `vault`. When F9.3 starts, the shared payload sealing and key lifecycle package. |
| 3 | domain services | Own tables or domain behavior; implement or define ports. Domain isolation applies: layer 3 packages never import each other except where explicitly listed. |
| 4 | composition | `internal/app`: the sole composition root that wires layer 3 services together. Result gates, checks, and worktree integration are wired in `internal/app`. |
| 5 | transport | `internal/hostapi`: Connect handlers mapping proto messages to domain calls; generated proto code (`pkg/pb/...`). |
| 6 | entry / public | `cmd/fugue` (entry point and subcommands), development tools, and public client packages (`pkg/client`). |

### 2. Package table

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/process` | 1 | Supervised OS process trees: spawn, JSON-line reader (8 MiB), stderr ring (64 KiB), stop order (protocol interrupt -> SIGINT -> SIGTERM -> SIGKILL tree), Linux cgroups v2 (`systemd-run --user --scope`) stop proof, macOS `sysctl kern.proc` PID tracking. Owner: vendor runtime (VR-09). | `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/supervise` | F9.2 |
| `internal/gitops` | 1 | Git manager for multi-agent worktrees: per-repo mutex, `gc.auto=0`, ref-lock retries, worktree add/remove, and branch lifecycle. Owner: orchestration (OR-11). | `internal/platform/errcode`, `internal/platform/clock`, `internal/platform/supervise` | F11.4 |
| `internal/vendor` | 3 | Neutral vendor domain core: `Adapter` and `Session` ports (`vendor-runtime §6`), capability contracts, session state machine, and session actor supervising process and adapter. Owner: vendor runtime (VR-01, VR-07). | `internal/process`, `internal/transcript`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F9.2 |
| `internal/vendoradapter/claude` | 3 | Claude Code adapter: `-p` stream-json over stdio, normalizer into transcript items, rate limit events, slash commands, permission MCP prompt tool bridge. Owner: vendor runtime (G-04). | `internal/vendor`, `internal/process`, `internal/transcript`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F9.5 |
| `internal/vendoradapter/codex` | 3 | Codex adapter: `app-server` JSON-RPC over stdio, schema conformance fixtures, approval request routing, turn interrupt/steer, rate limits. Owner: vendor runtime (G-04). | `internal/vendor`, `internal/process`, `internal/transcript`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/supervise` | F9.7 |
| `internal/vendoradapter/agy` | 3 | Antigravity adapter: ACP v1 over stdio, `session/new`, `session/load`, permission request forwarding, available command updates. Owner: vendor runtime (G-04). | `internal/vendor`, `internal/process`, `internal/transcript`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/supervise` | F9.8 |
| `internal/vendorstore` | 3 | SQL persistence for vendor sessions, turns, domain events, transcript items, and team orchestration; sealed protected payloads under session keys (TR-04, INV-09). Owner: vendor runtime & storage (VR-01, TR-04). | `internal/storage`, `internal/sealing`, `internal/crypto`, `internal/vault`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F9.3 |
| `internal/transcript` | 3 | Neutral transcript item model, read model projection, coalesced delta topic (30/s), pagination and truncation limits, display redaction. Owner: transcript (H-48). | `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F9.4 |
| `internal/teams` | 3 | Multi-tier team orchestration (depth <= 2: lead, team manager, member), task and team state machines, role catalog in workspace config, mailbox wake-up, exception routing, and delegation contracts. Owner: orchestration (H-49). | `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F11.1 |
| `pkg/pb/...` | 5 | Generated protobuf and Connect code (all subpackages). Never edited by hand. Owner: Host API. | `pkg/pb/...` | F9.9 or F10.2 (whichever first needs a client) |
| `pkg/client` | 6 | Public Go client library for the Host API, supporting both `local` and `session` principals, socket dialing, token credentials, and subscription catch-up. Owner: client. | `pkg/pb/...` | F9.9 or F10.2 (whichever first needs a client) |

### Amendments

2026-11-12 (F6.3b): add `internal/environments` at layer 1 for the environment tag registry and confirmation policies for targets.

2026-11-09 (F9.5a): the concrete adapters move from `internal/vendor/<name>` to
`internal/vendoradapter/<name>` (claude now, codex and agy per their phases).
The Go toolchain does not descend into directories named `vendor` when
expanding `...` patterns (`go help packages`: directories named "vendor" are
ignored), so every wildcard-driven gate — `go list ./...` behind the arch
tests, lint, coverage, deadcode, fuzz discovery and the vendor-credential scan
— silently skips packages below `internal/vendor`. The port stays at
`internal/vendor` (the directory itself matches); only subpackages need the
new parent, which parallels `internal/modeladapter`. No import edge, layer or
table ownership changes; the three adapter rows above already carry the new
paths.

2026-11-08 (F9.3b): `internal/vendorstore` may import `internal/sealing` for protected payloads and session-key lifecycle.

2026-11-07 (F6.7a; owner approval requested on the PR): add the pure, no-I/O
content-safety detector. `internal/platform/redact` is needed solely for
`redact.Secret` inputs so known values cannot leak through formatting; log/error
redaction remains separate. No other internal imports are permitted.

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/environments` | 1 | Pure logic, no I/O: environment tag registry and confirmation policies for targets. Owner: interaction (I-03, §3.4). | `internal/platform/errcode` | F6.3b |
| `internal/secrets` | 1 | Pure logic, no I/O: known-value, token-pattern and key-name secret detection. Owner: content safety (H-27). | `internal/platform/errcode`, `internal/platform/redact` | F6.7a |
| `internal/sealing` | 2 | Protected payload sealing and session key lifecycle. Owner: storage security (G-03). | `internal/crypto`, `internal/vault`, `internal/platform/errcode`, `internal/platform/ids`, `internal/platform/redact` | F9.3a |

Directories outside `internal/` and `pkg/` governed by this ADR:

| Path | Role |
| --- | --- |
| `cmd/fugue` (`mcp-bridge`) | Layer 6 subcommand `fugue mcp-bridge`. Runs stdio MCP server for vendor CLIs, authenticating to the Host API via `pkg/client` using a per-session token (`principal = session`). |
| `ui/` | External Go module (`github.com/syurodev/fugue/ui`). Contains the Gio desktop app (`fugue-app`). Isolated in its own module to contain cgo and graphics libraries. Never imported by the core module. |

### 3. Rules this table creates

1. **Domain isolation**: Layer 3 domain services (`internal/teams`, `internal/vendorstore`, `internal/transcript`) do not import each other, nor do they import `internal/vendor`. They remain decoupled and interact only through composition in `internal/app`.
2. **Adapter sideways edge**: `internal/vendoradapter` and its concrete adapter subpackages (`internal/vendoradapter/claude`, `internal/vendoradapter/codex`, `internal/vendoradapter/agy`) may import `internal/transcript` solely for the neutral transcript item and delta types emitted by adapter normalizers. Adapters never import `internal/vendorstore`.
3. **Single composition root**: Only `internal/app` (layer 4) wires layer-3 services together. Result gates, checks, and worktree operations are composed in `internal/app`.
4. **Public packages never import internal**: No package under `pkg/` may import anything under `internal/`. Architecture tests in `internal/arch` enforce this invariant.
5. **Core isolation from UI**: The core module `github.com/syurodev/fugue` must never import `github.com/syurodev/fugue/ui` or any package under `ui/`. Architecture tests enforce this invariant.
6. **Supervisor and git manager encapsulation**: `internal/process` and `internal/gitops` are layer-1 foundation packages. They purely manage process lifecycle and git worktree operations respectively, importing only platform packages and zero domain services.

### 4. Where `database/sql` and the SQLite driver may be used

- `modernc.org/sqlite` continues to be imported exclusively by `internal/storage`.
- `database/sql` is imported by `internal/vendorstore` for executing queries against the tables it owns, joining the existing allowlist (`storage`, `vault`, `credentials`, `execstore`, `workspacememory`).
- All DDL remains strictly centralized: `internal/storage/migrations/*.sql` owns all `CREATE TABLE` and migration logic. `internal/vendorstore` never issues DDL.

### 5. Table ownership

One owning package per table. Only `internal/vendorstore` reads and writes `vendor_*` and `team_*` tables; other packages interact through `vendorstore` methods:

| Table | Owner | Phase |
| --- | --- | --- |
| `vendor_sessions` | `internal/vendorstore` | F9.3 |
| `vendor_turns` | `internal/vendorstore` | F9.3 |
| `vendor_events` | `internal/vendorstore` | F9.3 |
| `vendor_transcript_items` | `internal/vendorstore` | F9.3 |
| `vendor_payloads` | `internal/vendorstore` | F9.3 |
| `vendor_session_keys` | `internal/vendorstore` | F9.3 |
| `vendor_purges` | `internal/vendorstore` | F9.3 |
| `vendor_session_changes` | `internal/vendorstore` | F9.9c |
| `team_records` | `internal/vendorstore` | F11.1 |
| `team_tasks` | `internal/vendorstore` | F11.1 |
| `team_task_events` | `internal/vendorstore` | F11.1 |
| `team_mailbox_messages` | `internal/vendorstore` | F11.1 |

`vendorstore` owns `vendor_payloads`, `vendor_session_keys`, and `vendor_purges`, but will not duplicate the sealing code. When F9.3 starts, the session-key wrap, AEAD seal/open, size limits, and crypto-shred logic of `execstore` move into one shared layer-2 package used by both stores. `execstore` only changes its imports, and its existing tests must stay green unchanged.

### 6. Third-party modules

Licenses adhere to `quality-gates` §7 (MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC).

#### Core module (`github.com/syurodev/fugue`)
For roadmap items F9–F11, **none, stdlib only**:
- Process tree supervision (F9.2) uses standard library `os/exec`, `os`, `syscall`, `bufio`, and existing direct dependency `golang.org/x/sys/unix` (BSD-3-Clause, already approved in F1.3) for macOS `sysctl kern.proc` and Linux cgroups v2 file I/O.
- Claude stream-json (F9.5), Codex JSON-RPC 2.0 (F9.7), Antigravity ACP v1 (F9.8), and MCP bridge (F9.6) are implemented directly with stdlib `encoding/json` and `io`.

#### Desktop UI module (`github.com/syurodev/fugue/ui`, enters in F10.2)
Third-party dependencies required for `ui/go.mod` (does not affect the core module):

| Module | License | Reason | Enters |
| --- | --- | --- | --- |
| `gioui.org` | Unlicense OR MIT | Pure Go immediate-mode GPU GUI framework (DA-01) | F10.2 |
| `github.com/yuin/goldmark` | MIT | CommonMark markdown parser for custom Gio markdown renderer (DA-03) | F10.2 |

Bundled static assets in `ui/`:
- Inter font (OFL-1.1, approved open font license, DA-04).
- JetBrains Mono font (OFL-1.1, approved open font license, DA-04).

## Consequences

- The vendor runtime domain operates cleanly alongside native execution components without cross-contamination.
- `internal/arch` tests (`TestLayerGraph` and `TestTableOwnership`) enforce layer boundaries, table access, and the `pkg/` non-internal import invariant across both ADR 0001 and ADR 0002.
- Amendments to `noprotoleak` and ADR 0001 §6 to permit `pkg/client` and `cmd/*` to import protobuf and Connect landed in the PR that moved generated code (F10.2a, 2026-11-08): generated code lives in `pkg/pb`, `noprotoleak` allows the importers above, and `pkg/` still never imports `internal/`.
- The core module has no cgo dependency, and the `ui/` module's cgo build gets its own workflow in F10.2 (`task test` in the core module uses `-race`, which needs cgo, so core CI is not `CGO_ENABLED=0`).
- The Host API client becomes reusable across desktop UI, CLI commands, and MCP bridge tools through a unified `pkg/client` package.

## Decisions recorded at approval

1. **Packages, layers and import edges:**
   - `internal/process` (layer 1) manages process trees, line reading, and stop orders.
   - `internal/gitops` (layer 1, F11.4) manages git worktrees, per-repo mutex, `gc.auto=0`, and ref-lock retries.
   - `internal/vendor` and its adapter subpackages (`claude`, `codex`, `agy`) live at layer 3. They may import `internal/transcript` for normalized item/delta types.
   - Purity is not enforced for `internal/vendor` (which holds the session actor) or `internal/teams`; purity rules can be added later if an explicit `kernelpure` scope change is approved.
   - Result gates, checks, and worktree operations are wired in `internal/app`.
2. **Tables and protected payloads:**
   - `internal/vendorstore` owns `vendor_payloads`, `vendor_session_keys`, and `vendor_purges`, but code is not copied.
   - When F9.3 starts, the session-key wrap, AEAD seal/open, size limits, and crypto-shred logic of `execstore` move into a shared layer-2 package used by both `execstore` and `vendorstore`. `execstore` only changes its imports, and its existing tests must stay green unchanged.
3. **Public client and proto code:**
   - `pkg/pb/...` (layer 5) and `pkg/client` (layer 6) enter the layer table with their own rows and import lists.
   - Architecture rule enforced by `internal/arch`: no package under `pkg/` may import anything under `internal/`.
   - Amendments to `noprotoleak` and ADR 0001 §6 land in the PR that moves generated code (F9.9 or F10.2, whichever first needs a client).
4. **The `ui/` module:**
   - Dedicated `ui/go.mod` without `go.work`, using `replace github.com/syurodev/fugue => ../`.
   - The core module has no cgo dependency; `ui/`'s cgo build gets its own workflow in F10.2.
5. **`fugue mcp-bridge`:**
   - Subcommand in `cmd/fugue/` (layer 6) using `pkg/client` with the per-session token (`principal = session`).
6. **Third-party modules:**
   - Core module: none, stdlib only. `ui/` module: Gio, goldmark, and OFL fonts approved for entry in F10.2.
