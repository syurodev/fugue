# ADR 0001: Package layout and layering

- **Status:** Approved by the user, 2026-11-01 (with the amendments recorded in "Decisions recorded at approval")
- **Date:** 2026-11-01
- **Roadmap item:** F0.2 (this repository's gate G-01)
- **Design:** `architecture` §4 and §8, `host-api` §4 and §8, `storage-security` §2, §3 and §9,
  `event-storage-recovery` §3 and §5, `continuation` §5, `coordination` §3.

## Context

Fugue is one Go module, `github.com/syurodev/fugue`, built as an all-in-one core: a single
host process owns the data directory, the vault, the execution store and every service, and
clients reach it only through the Host API. The design was written for DevKit and leaves package
placement open ("open decision before implementation"). This ADR closes that decision so that
`internal/arch` (`TestLayerGraph`, `TestTableOwnership`, `TestPackageDocs`) can enforce it from
the first line of code.

The tables below are **machine-read** by `internal/arch`. Keep their column order and the
back-ticked package and table names. A row may name a package that does not exist yet (its
`Phase` says when it appears); an existing package without a row fails the test.

## Decision

### 1. Layers

A package may import only the internal packages in its **May import** column, and every listed
edge must point to the same or a lower layer; the import graph must stay acyclic. Standard
library and third-party imports are governed separately (section 4 and `depguard`).

| Layer | Name | Rule |
| --- | --- | --- |
| 0 | platform | Stdlib only. No I/O beyond what the package exists for. |
| 1 | foundation | Pure logic or one narrow OS concern (`kernel` is pure, `config` reads the environment, `crypto` does cryptography, `storage` opens the database). |
| 2 | secured stores | Built on layer 1: `codec`, `vault`. |
| 3 | domain services | Own tables or behavior; implement or define ports; never import each other except where listed. |
| 4 | composition | `app`: the only place that wires layer 3 together. |
| 5 | transport | `hostapi`: Connect handlers; the only importer of generated proto code. |
| 6 | entry | `cmd/*`: process entry points. |

### 2. Package table

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/platform/errcode` | 0 | Typed errors with stable codes and the public registry. Owner: error contract (Host API `keptCodes`). | | F0.8 |
| `internal/platform/redact` | 0 | `Secret` and redacting `slog` handler. Owner: security invariants. | | F0.8 |
| `internal/platform/clock` | 0 | `Clock` port, real clock and fake clock. Owner: time port. | | F0.8 |
| `internal/platform/ids` | 0 | ID and randomness port, real and deterministic generators. Owner: ID port. | `internal/platform/clock` | F0.8 |
| `internal/platform/supervise` | 0 | `supervise.Go(ctx, name, fn)`: the only place a goroutine starts. Owner: concurrency rules. | `internal/platform/errcode` | F0.8 |
| `internal/config` | 1 | Typed configuration; the only reader of environment variables. Owner: configuration port. | `internal/platform/errcode`, `internal/platform/redact` | F0.8 |
| `internal/kernel` | 1 | Pure `decide` and `evolve`, commands, events, invariants, and its own rejection type with stable codes. Owner: state machine (G-02). | | F2.1 |
| `internal/crypto` | 1 | XChaCha20-Poly1305 AEAD, Argon2id, envelope, keyring. Owner: storage security (G-03). | `internal/platform/errcode`, `internal/platform/redact` | F1.1 |
| `internal/storage` | 1 | Opens SQLite, checks pragmas, runs migrations (the only `CREATE TABLE`), data-directory lock, host incarnation. Owner: storage (G-03). | `internal/platform/errcode`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise`, `internal/config` | F1.3 |
| `internal/interaction` | 1 | In-memory approval and selection requests per incarnation, receipts, answer once. Owner: interaction (G-05). | `internal/platform/errcode`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F1.5 |
| `internal/sandbox` | 1 | Sandbox policy, seatbelt, bwrap and landlock backends, process runner. Owner: tool system (G-04). | `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/supervise` | F1.6 |
| `internal/snapshot` | 1 | Checkpoint shadow git. Owner: tool system (G-04). | `internal/platform/errcode`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F1.7 |
| `internal/codec` | 2 | Canonical codec v1 and receipt keys. Owner: storage (G-03). | `internal/kernel`, `internal/platform/errcode` | F2.3 |
| `internal/vault` | 2 | Master Password, lock state, `SealRecord`, observation API. Owner: storage security (G-03). | `internal/crypto`, `internal/storage`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F1.2 |
| `internal/credentials` | 3 | Model provider accounts and `PickAccount`. Owner: model adapter (G-04). | `internal/vault`, `internal/storage`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids` | F1.4 |
| `internal/execstore` | 3 | SQL for the `exec_*` tables, protected payloads, crypto-shred. Implements the coordinator's store port. Owner: storage (G-03). | `internal/kernel`, `internal/codec`, `internal/crypto`, `internal/vault`, `internal/storage`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F2.4 |
| `internal/coordinator` | 3 | Per-session actor, dispatch, timers, scheduler; defines the ports for store, model, tools and interaction in kernel types. Owner: execution (G-02). | `internal/kernel`, `internal/codec`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F2.6 |
| `internal/contextengine` | 3 | Context assembly L0 to L5, rules, compaction. Owner: context engine. | `internal/kernel`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids` | F4.5 |
| `internal/modeladapter` | 3 | Neutral model contract and error classification. Owner: model adapter (G-04). | `internal/kernel`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F2.7 |
| `internal/modeladapter/faux` | 3 | Scripted provider for headless tests. Owner: model adapter (G-04). | `internal/modeladapter`, `internal/kernel`, `internal/platform/errcode`, `internal/platform/clock`, `internal/platform/supervise` | F2.7 |
| `internal/modeladapter/anthropic` | 3 | Anthropic adapter. Owner: model adapter (G-04). | `internal/modeladapter`, `internal/kernel`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/supervise` | F4.2 |
| `internal/modeladapter/openai` | 3 | OpenAI adapter. Owner: model adapter (G-04). | `internal/modeladapter`, `internal/kernel`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/supervise` | F4.3 |
| `internal/tools` | 3 | Tool registry, descriptors, binding revision. Concrete tools are subpackages with their own rows added by the PR that introduces them. Owner: tool system (G-04). | `internal/kernel`, `internal/interaction`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F2.8 |
| `internal/tools/pure_read` | 3 | Deterministic in-memory read tool. Owner: tool system (G-04). | `internal/kernel`, `internal/platform/errcode` | F2.8 |
| `internal/workspacememory` | 3 | Workspace memory entries, history, CAS by revision. Owner: context engine. | `internal/storage`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids` | F4.7 |
| `internal/app` | 4 | Composition root and application services. Owner: application layer. | `internal/kernel`, `internal/codec`, `internal/crypto`, `internal/vault`, `internal/storage`, `internal/credentials`, `internal/execstore`, `internal/coordinator`, `internal/contextengine`, `internal/modeladapter`, `internal/modeladapter/faux`, `internal/modeladapter/anthropic`, `internal/modeladapter/openai`, `internal/tools`, `internal/interaction`, `internal/sandbox`, `internal/snapshot`, `internal/workspacememory`, `internal/config`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise` | F5.1 |
| `internal/hostapi` | 5 | Connect handlers; maps proto types to domain types and calls `app`. Owner: Host API. | `internal/app`, `internal/kernel`, `internal/hostapi/internal/pb/...`, `internal/platform/errcode`, `internal/platform/redact`, `internal/platform/clock`, `internal/platform/ids`, `internal/platform/supervise`, `internal/config` | F5.1 |
| `internal/hostapi/internal/pb/...` | 5 | Generated protobuf and Connect code (all subpackages). Never edited by hand. Owner: Host API. | | F0.7 |
| `internal/arch` | test | Architecture tests only; reads the graph with `go list`. Owner: quality gates. | | F0.5 |

Directories outside `internal/` and not covered by this table:

| Path | Role |
| --- | --- |
| `cmd/fugue` | Layer 6. `main`: `fugue serve` and subcommands. Imports `internal/app`, `internal/hostapi`, `internal/config` and `internal/platform/...`. Only place `context.Background`, `fmt.Print*` and `panic` in `must*` helpers are allowed. |
| `cmd/fuguegate` | Layer 6, development tool (F0.3). Runs the gates no off-the-shelf tool covers: coverage floor and baseline, short fuzzing, mutation testing, dead code. Imports no `internal/*` package. |
| `tools/fuguevet` | Custom analyzers (`go vet -vettool`). Imported by nothing; imports no `internal/*` package except through test fixtures. |
| `proto/fugue/<module>/v1` | `.proto` sources and `buf` configuration. |
| `quality/` | Gate data (`coverage.json`, `ratchet.json`) and gate self-tests with their violating fixtures under `testdata`. No production code. |
| `docs/` | Roadmap, quality gates, dependencies, ports, `adr/`. |

### 3. Rules this table creates

1. `TestLayerGraph`: every package directly or transitively under `internal/` has a row; every
   import between two module packages appears in the importer's **May import** column; a listed
   edge never points to a higher layer; no import cycles. `...` at the end of a path matches the
   whole subtree.
2. `internal/kernel` imports only pure stdlib and no other package of this module. `fuguevet`
   `kernelpure` enforces it (quality gates §4). Rejections are a kernel type carrying a stable
   code; `app` and `hostapi` translate them to `errcode` values at the boundary.
3. Generated proto code lives in `internal/hostapi/internal/pb`, so Go's `internal` rule already
   stops every other package from importing it; `noprotoleak` is the second line of defence.
4. Ports are defined by the consumer (`coordinator`) and expressed only in kernel types, so
   `execstore`, `modeladapter` and `tools` implement them without importing `coordinator`. They
   are wired together only in `app`.
5. Layer 3 packages do not import each other except `modeladapter/*` importing their parent.
6. A new package, or a new edge, is added by a row change in this ADR in the same PR. The PR
   needs the user's approval like any gate-defining change (`CODEOWNERS` covers `docs/adr/**`).

### 4. Where `database/sql` and the SQLite driver may be used

`modernc.org/sqlite` is imported only by `internal/storage`. `database/sql` is imported only by
`internal/storage`, `internal/vault`, `internal/credentials`, `internal/execstore` and
`internal/workspacememory`, each touching only the tables it owns. These five are the
allowlist for `TestTableOwnership`.

`storage.Open` returns the single connection handle (`journal_mode=DELETE`,
`synchronous=FULL`, file `0600`, per `storage-security` §2) and owners receive it by injection.
All DDL lives in `internal/storage/migrations/*.sql`; an owner never creates tables itself.

### 5. Table ownership

One owning package per table. Only the owner has SQL that reads or writes the table; other
packages use the owner's API. Names are final once this ADR is approved; a rename needs an
amendment. Column details are fixed by the migration, not here.

| Table | Owner | Phase |
| --- | --- | --- |
| `schema_migrations` | `internal/storage` | F1.3 |
| `exec_host` | `internal/storage` | F1.3 |
| `vault_meta` | `internal/vault` | F1.2 |
| `credential_accounts` | `internal/credentials` | F1.4 |
| `exec_admission` | `internal/execstore` | F2.4 |
| `exec_runs` | `internal/execstore` | F2.4 |
| `exec_events` | `internal/execstore` | F2.4 |
| `exec_invocations` | `internal/execstore` | F2.4 |
| `exec_attempts` | `internal/execstore` | F2.4 |
| `exec_timers` | `internal/execstore` | F2.6 |
| `exec_time_windows` | `internal/execstore` | F3.4 |
| `exec_receipts` | `internal/execstore` | F2.4 |
| `exec_context_decisions` | `internal/execstore` | F4.5 |
| `exec_transcript` | `internal/execstore` | F2.4 |
| `exec_queue` | `internal/execstore` | F5.4 |
| `exec_goals` | `internal/execstore` | F5.5 |
| `exec_goal_events` | `internal/execstore` | F5.5 |
| `exec_payloads` | `internal/execstore` | F2.4 |
| `exec_session_keys` | `internal/execstore` | F2.4 |
| `exec_purges` | `internal/execstore` | F2.4 |
| `workspace_memory_entries` | `internal/workspacememory` | F4.7 |
| `workspace_memory_history` | `internal/workspacememory` | F4.7 |

Q-13 (user, 2026-11-03, option A): the F2.4 phase for `exec_payloads`,
`exec_session_keys` and `exec_purges` introduces physical schema, constraints, trigger support
and the transaction boundary for already sealed records. F2.5 retains session-key creation,
wrapping/unwrapping, payload seal/open, vault-lock guards and key-cache zeroing, size limits,
purge orchestration and crypto-shred evidence. Until that integration exists, production
protected commands fail closed; F2.4 atomicity tests do not establish complete key-lifecycle
security. Table ownership and the single-transaction security contract are unchanged.

`exec_host` belongs to `storage` because the host incarnation is created only while the
data-directory lock is held and the lock is `storage`'s; `execstore` reads the incarnation
through `storage`'s API. The interaction owner keeps requests in memory and has no table
(`tool-system` §5).

### 6. Initial third-party modules

Licenses follow `quality-gates` §7. "Enters" is the roadmap item that first adds the module to
`go.mod`; the row in `docs/dependencies.md` is added by that item. The approver for everything
on this list is the user, by approving this ADR, with the exception flagged below.

| Module | License | Reason | Enters | Importable by |
| --- | --- | --- | --- | --- |
| `go.uber.org/goleak` | MIT | `TestMain` leak check in every package | F0.3 | tests only |
| `github.com/google/go-cmp` | BSD-3-Clause | Test comparisons | F0.3 | tests only |
| `golang.org/x/tools` | BSD-3-Clause | `go/analysis` for `fuguevet` | F0.4 | `tools/fuguevet` |
| `google.golang.org/protobuf` | BSD-3-Clause | Generated message code | F0.7 | `internal/hostapi/...` |
| `connectrpc.com/connect` | Apache-2.0 | Connect handlers and clients | F0.7 | `internal/hostapi/...` |
| `golang.org/x/crypto` | BSD-3-Clause | Argon2id, XChaCha20-Poly1305 | F1.1 | `internal/crypto` |
| `modernc.org/sqlite` | BSD-3-Clause | Pure-Go SQLite driver | F1.3 | `internal/storage` |
| `golang.org/x/sys` | BSD-3-Clause | `flock`, peer credentials, landlock | F1.3 | `internal/storage`, `internal/sandbox`, `internal/hostapi` |
| `golang.org/x/net` | BSD-3-Clause | `h2c` for Connect over a unix socket | F5.1 | `internal/hostapi` |
| `github.com/anthropics/anthropic-sdk-go` | MIT | Anthropic adapter | F4.2 | `internal/modeladapter/anthropic` |
| `github.com/openai/openai-go` | Apache-2.0 | OpenAI adapter | F4.3 | `internal/modeladapter/openai` |
| `pgregory.net/rapid` | **MPL-2.0** | Property tests for the kernel (`quality-gates` §6) | F2.2 | tests only |

`pgregory.net/rapid` is MPL-2.0, outside the MIT, BSD, Apache-2.0, ISC list. It is the library
`quality-gates` §6 names for F2.2, so it is **not approved by this ADR**; the user decides at
F2.2 (see `roadmap` §7).

Dev tools pinned with `tool` directives in `go.mod` (not linked into the binary): `buf`
(Apache-2.0), `gofumpt` (BSD-3-Clause), `deadcode` and `govulncheck` (BSD-3-Clause),
`go-licenses` (Apache-2.0), `gremlins` (Apache-2.0), `protoc-gen-go` (BSD-3-Clause),
`protoc-gen-connect-go` (Apache-2.0), `task` (MIT). `golangci-lint` is pinned by exact version
in `Taskfile.yml` and CI because its module graph is not meant to be vendored into `go.mod`.

## Consequences

- The first packages (F0.8) compile into a layer 0 and layer 1 that already match this table.
- Adding a package or an edge is a visible, reviewed diff to this file.
- Deviations from the roadmap §5 sketch, each deliberate:
  - `config` is `internal/config`, not `internal/platform/config`, because `quality-gates` §3
    names `internal/config` as the only reader of environment variables.
  - Errors live in `internal/platform/errcode` (the sketch says "errors").
  - `storage` owns `exec_host`; `execstore` owns the other `exec_*` tables.
  - Generated code lives under `internal/hostapi/internal/pb`; TypeScript output is outside the
    Go tree and its path is set in F0.7.

## Decisions recorded at approval

1. `config` lives at `internal/config`. The roadmap §5 sketch is updated to match.
2. The kernel is stricter than first proposed: it imports **no** package of this module, not even
   `errcode`. It owns a rejection type with stable codes instead.
3. `exec_host` belongs to `storage`; the other `exec_*` tables belong to `execstore`.
4. Generated proto code lives under `internal/hostapi/internal/pb`.
5. `TestDependencyAllowlist` requires a row for each module that `go.mod` requires directly
   (including modules added by `tool` directives); `// indirect` modules are covered by
   `go-licenses check` and `govulncheck`.
6. `pgregory.net/rapid` (MPL-2.0) is not approved by this ADR; the user decides at F2.2
   (roadmap Q-06).
7. The DB file name stays open as Q-03; it does not affect the layout.
8. Q-13 option A approved by the user, 2026-11-03: move only the physical payload/key/purge
   support schema and sealed-record persistence boundary to F2.4; cryptographic lifecycle and
   purge behavior remain F2.5 as documented in section 5.
