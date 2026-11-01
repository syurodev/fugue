# Roadmap

The living work board of the repository. It answers: what to do, in what order, based on which
design, what counts as done, and where each item stands. This page **does not** change the
design.

Last updated: 2026-11-01, by the designer (initial version).

## 0. Context

- **Product:** Fugue by SynX. This repository is the **all-in-one Go core**: one host process
  (`fugue serve`) owns the data directory, the vault, the store and every service; the desktop
  shell (Electron) and other clients talk to it through the Host API.
- **DevKit (Wails)** is frozen at its current release and no longer developed. This repository
  **does not** import or depend on `syurodev/dev-kit`.
- **Goal of this phase:** the native harness (stage A of the core harness design). The
  dependencies the harness needs are ported **first**.
- **Design** lives in the base-doc repository under `content/projects/dev-kit/core-harness/`.
  The tables below cite pages by name, for example `state-machine §3`. The design was written
  for DevKit, so its path names (`internal/...`, `devkit.db`, `AppService`) are superseded by
  this repository's ADRs; semantics stay the same. Real semantic differences are listed in
  [§7](#7-needs-decision).
- **No harness code is carried over** from dev-kit (`internal/harnessruntime`,
  `internal/execstore`, `internal/hostapi` from PRs #412 to #420). The harness is written from
  scratch.
- **Data starts clean:** data directory `os.UserConfigDir()/Fugue/`, new schema. Importing
  DevKit data is a separate, later item.

## 1. Rules for the implementing agent

1. **Read before working:** `AGENTS.md`, [quality gates](quality-gates.md), the relevant ADRs in
   `docs/adr/`, and exactly the design pages the item points to.
2. **Work only on items whose design is settled.** Items in `Awaiting design` do not start.
3. **Never change design or gates on your own.** When something is wrong, missing or
   contradictory, move the item to `Needs decision`, record the question in
   [§7](#7-needs-decision), and take another unblocked item.
4. **Roadmap work: one item, one branch, one PR** into `main`, branch `<type>/<id>-<slug>` (for
   example `feat/f2.1-kernel-types`). A change outside the roadmap (bug, chore, docs) is also
   allowed as its own branch and PR, without an item ID. Never stack a PR on an unmerged PR.
   Keep PRs under roughly 600 changed lines, excluding generated code, golden files and
   testdata.
5. **Done** means: merged into `main`, every CI gate green, and the tests in the Evidence column
   exist and pass. `TestRoadmapEvidence` checks this.
6. **Update this page in the same PR:** status, PR link, evidence test names, one log line.
7. **Port from DevKit** under [§3](#3-porting-rules).

**Statuses:** `Not started` · `In progress` · `In review` · `Done` · `Blocked` (name the
blocking ID) · `Needs decision` · `Awaiting design`.

## 2. Phase overview

| Phase | Goal | Exit |
| --- | --- | --- |
| F0 | Repository foundation: names, layout, every quality gate, CI, platform primitives | `task check` green on a nearly empty repository; every gate has a negative test |
| F1 | Port dependencies: crypto, vault, storage and data-directory lock, credentials, interaction, sandbox, snapshot | Each package passes the same gates as new code; vector tests prove behavior is preserved |
| F2 | Headless vertical slice: kernel, codec, execution store, coordinator, faux model, fake read tool, continuation | Trace A reproduced exactly; replay calls neither model nor tool |
| F3 | Recovery, retry, timers, budgets and fault injection | Traces B and C; every row of the validation criteria has a test |
| F4 | Context engine and real model adapters | Evidence tables of the context engine and model adapter |
| F5 | Host API (`fugue serve`) and input coordination | Evidence tables of the Host API and input coordination |
| F6 | Tools and interaction | Awaiting designs H-17 to H-20, H-24, H-25 |
| F7 | Electron shell and session UI | Awaiting designs H-33, H-44 |
| F8 | Stage A release | Awaiting decision Q-02 |

## 3. Porting rules

Applies to all code copied from `syurodev/dev-kit`.

1. Copy **only what is needed**, one small package at a time; one PR per package.
2. Record the source in `docs/ports.md`: original path, dev-kit commit SHA, what was dropped or
   changed.
3. Remove every DevKit dependency: Wails, `bridge`, `app`, `AppService`, `DEVKIT_*` variables,
   the DevKit name.
4. Ported code passes **exactly the same gates as new code**: lint, coverage, mutation, `nolint`
   ratchet. Never add `nolint` to get a port through.
5. Preserve behavior with **vector tests**: before changing anything, write tests that capture
   the original code's output (for example ciphertext for a fixed key and nonce, a generated
   sandbox profile); those tests must still pass after the port.
6. Any change in behavior or format needs an ADR.
7. `crypto`, `vault` and `sandbox` PRs need the user's approval.

## 4. Work items

### F0 — Foundation

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F0.1 | Repository name, Go module path, LICENSE, README | dev-kit Fugue naming ADR | Q-01 | `go.mod` has the canonical module path; LICENSE present | Done | [#1](https://github.com/syurodev/fugue/pull/1) | Q-01 answered 2026-11-01 |
| F0.2 | Layout and layering ADR (this repository's G-01): package tree, layer table, owner of every SQL table, initial third-party dependencies | `architecture`, `host-api §4`, `storage-security` | F0.1 | ADR `docs/adr/0001-package-layout.md` approved by the user | Done | [#2](https://github.com/syurodev/fugue/pull/2) | ADR approved by the user 2026-11-01; the [§5](#5-proposed-layout) sketch is superseded where the ADR differs |
| F0.3 | Toolchain: `go`/`toolchain`, `tool` directives, `Taskfile.yml` (`check`, `fix`, `test`, `lint`), `.golangci.yml` per gates §2–3 | quality-gates §1–3, §7 | F0.2 | `task check` green; every enabled linter has a violating fixture that is caught: `TestLintFixtures`, `TestEveryEnabledLinterHasAFixture`, `TestFormattersFlagUnformattedFiles`; module gates: `TestTidyGate`, `TestVerifyGate`, `TestGovulncheckGate`, `TestLicenseGate`, `TestTaskfileRunsEveryGate`; tool gates: `TestDeadcodeGate`, `TestFuzzGate`, `TestMutationGate`, `TestEvaluate` | Done | [#4](https://github.com/syurodev/fugue/pull/4) | `cmd/fuguegate` carries the gates no tool covers (coverage, fuzz, mutation, deadcode). `fuguevet` and `arch` steps join `task check` in F0.4 and F0.5, `proto` in F0.7. PR is above the 600-line guideline because it carries the gate self-tests |
| F0.4 | `tools/fuguevet`: `kernelpure`, `nobarego`, `secretlog`, `todoref`, `noprotoleak` | quality-gates §4 | F0.3 | `analysistest` valid and violating cases for each analyzer: `TestKernelPure`, `TestNoBareGo`, `TestSecretLog`, `TestTodoRef`, `TestNoProtoLeak`, `TestLeaks`, `TestInTree`; the binary registers all five: `TestVettoolRunsEveryAnalyzer` | In review | [#5](https://github.com/syurodev/fugue/pull/5) | `vet` step added to `task check`. Q-08 records how `secretlog` reads the gate wording |
| F0.5 | `internal/arch`: every test in quality-gates §5, ratchets in §8 | quality-gates §5, §8 | F0.2, F0.3 | Each test has a violating fixture | Not started | | `TestRoadmapEvidence` reads this page |
| F0.6 | PR and nightly CI, `CODEOWNERS`, `gate-guard`; the user enables branch protection | quality-gates §9–10 | F0.3–F0.5 | A trial PR violating each gate is blocked | Not started | | Branch protection is a GitHub setting the user changes. Also: a `commit-lint` job rejects any PR commit or PR title containing `Co-Authored-By` and checks that the PR title is a Conventional Commit (no item ID required); a local `commit-msg` hook, installed by `task hooks`, applies the same rules |
| F0.7 | `proto/fugue/` skeleton with `buf` (lint, breaking, Go and TS generation for Connect), no services yet | `host-api §4, §8` | F0.2 | `proto` job green; stale generated code fails | Not started | | |
| F0.8 | Platform primitives: `clock` (real and fake), `ids`, coded errors and registry, `redact.Secret`, `supervise`, `config` | quality-gates §3–4 | F0.3 | Tests for each primitive; the fake clock drives timers | Not started | | |

### F1 — Port dependencies

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F1.1 | `crypto`: XChaCha20-Poly1305 AEAD, Argon2id KDF, envelope, keyring | `storage-security §4` | F0.8 | Vector tests from dev-kit `internal/crypto`; envelope fuzzing | Not started | | User approval |
| F1.2 | `vault`: Master Password, locked on every process start, `SealRecord`; observation API (snapshot, revision, subscription, lock guard) | `storage-security §8` | F1.1 | Unlock before recovery, lock while in use, slow subscriber | Not started | | Observation API written fresh from the design, not copied from PR #413 |
| F1.3 | `storage`: open SQLite (`modernc.org/sqlite`), pragma checks, migration framework, `Fugue/` data directory, `fugue.lock`, incarnation | `storage-security §2–3, §9` | F0.2, F0.8 | Wrong pragma rejected; two processes, path aliases, crash then reacquire the lock | Not started | | DB file name per Q-03 |
| F1.4 | `credentials`: model provider accounts (API key in the vault, Sign in with ChatGPT tokens), `PickAccount` | `model-adapter §7, §11` | F1.2, F1.3 | No secret in logs or errors; account choice follows rate limits | Not started | | Only what the model adapter needs |
| F1.5 | `interaction`: in-memory requests per incarnation, receipts, answer-once | `tool-system §5`, D-16 | F0.8 | An old request cannot be answered after restart; a duplicate answer is rejected | Not started | | Full API awaits H-24 |
| F1.6 | `sandbox`: policy, seatbelt, bwrap and landlock backends, process runner | `tool-system §6` | F0.8 | Vector tests of generated profiles; smoke tests on macOS and Linux | Not started | | User approval; per-session jail awaits H-25 |
| F1.7 | `snapshot`: checkpoint shadow git, interface per the design | `tool-system §9` | F0.8 | Two separate quotas; restore touches only the tool's diff | Not started | | |

### F2 — Headless vertical slice

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F2.1 | Kernel: command and event types, `decide` and `evolve` for run, step, model and tool invocation/attempt; derived lifecycle, phase and `waitReasons` | `state-machine §1–7`, `event-catalog §1–5` | F0.4, F0.8 | Tables of valid and rejected transitions | Not started | | `kernelpure` must be green |
| F2.2 | Invariant checker SM-01 to SM-12; property tests over random command sequences | `state-machine §3` | F2.1 | Each invariant has a violating test; rapid runs invariants after every batch; replaying twice yields the same state | Not started | | |
| F2.3 | Canonical codec v1, receipt deduplication | `storage-security §5`, `event-storage-recovery §5` | F2.1 | Duplicate keys, unknown fields, wrong `codec_version`; the same `command_id` gets the same receipt; fuzzing | Not started | | |
| F2.4 | Execution store: `exec_*` tables, append-only triggers, one transaction for event, payload, projection, receipt and admission | `storage-security §2–3, §6`, `event-storage-recovery §5` | F1.3, F2.3 | Atomicity under fault injection; triggers block UPDATE and DELETE | Not started | | G-03 evidence |
| F2.5 | Protected payloads: session key wrapped with `SealRecord`, AEAD, size limits, crypto-shred | `storage-security §4, §7` | F1.2, F2.4 | Crypto-shred; 1 MiB, 64 MiB and 1 GiB limits | Not started | | G-03 evidence |
| F2.6 | Per-session coordinator: admission row, minimal queue, worker reservation, commit before handoff, read-back on unknown commit, durable timers | `event-storage-recovery §2–3, §7`, `architecture §3, §6` | F2.4 | An unknown commit never dispatches twice; a stale worker is fenced | Not started | | |
| F2.7 | Faux model provider: text, thinking, tool calls, every error class, cut streams | `model-adapter §9` | F2.1 | Usable by every headless test | Not started | | |
| F2.8 | Fake read tool (`pure_read`) on the `Prepare`/`Execute` contract | `tool-system §2` | F2.1 | Full check, ready, dispatch, result chain | Not started | | |
| F2.9 | After-step rules, `maxSteps` with `Waiting(step_budget)` and `BudgetExtended`, one automatic continuation on `length`, loop reminders | `continuation §2–4` | F2.1, F2.3 | Evidence in continuation §8 (except goal) | Not started | | Loop reminders need F4.5 |
| F2.10 | **End-to-end slice**: start, model, tool, model, finish (trace A); durable; replay without side effects | `event-catalog §7` | F2.2–F2.9 | Trace A has exactly 22 events; replay makes 0 model and tool calls | Not started | | Main F2 milestone |

### F3 — Recovery and fault injection

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F3.1 | Restart classification: Queued, `ToolCheckReset`, dispatching becomes unknown, vault locked, payload lost | `state-machine §8`, `event-storage-recovery §7` | F2.10 | Trace B; an unknown effect never reruns | Not started | | |
| F3.2 | Stop intent, cancel, quiesce, `AcceptUnknown`, `CloseRunWithoutPayload`, `SelectRunContext` | `state-machine §6`, `event-storage-recovery §3` | F3.1 | Trace C | Not started | | |
| F3.3 | Model retry: `Planned` attempt, retry intent and timer written in one batch before waiting; timer elapsed only opens dispatch | `event-catalog §3`, `model-adapter §3` | F2.10 | A crash while waiting creates no new attempt and charges nothing again; a stop while waiting cancels the attempt and the timer | Not started | | |
| F3.4 | Active-time budget, tranches, crash mid-tranche | `state-machine §7` | F2.6 | Downtime is not charged; conservative charge on resume | Not started | | |
| F3.5 | Fault injection suite per the validation criteria table | `decisions-and-validation` (validation criteria) | F3.1–F3.4 | Every row of the table has a test; guards meet the mutation threshold | Not started | | Closes G-02 |

### F4 — Context engine and model adapters

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F4.1 | Neutral model contract, error classification | `model-adapter §2–3` | F2.10 | The faux provider covers every error class | Not started | | |
| F4.2 | Anthropic adapter (API key), converter, catalog, usage | `model-adapter §4–8, §10` | F4.1, F1.4 | Golden streams; signed thinking; SDK control conditions re-verified | Not started | | |
| F4.3 | OpenAI adapter: API key, then Sign in with ChatGPT | `model-adapter §11` | F4.1, F1.4 | Error table §11.4 has tests; no automatic failover on a ChatGPT plan | Not started | | UI in F7 |
| F4.4 | Account failover from rate limits | `model-adapter §7` | F4.2 | All accounts limited leads to `Waiting(retry_timer)` | Not started | | |
| F4.5 | Context engine: L0–L5, `ContextInjected`, `InstructionsLoaded`, rules R0–R3, token measurement | `context-engine §2–3, §5` | F2.10 | Deterministic assembly; patches leave the prefix unchanged | Not started | | |
| F4.6 | Pruning, compaction, language check | `context-engine §6` | F4.5, F4.1 | Context engine evidence table §9 | Not started | | |
| F4.7 | W tier: port `workspacememory`, pinned blocks, index, hash freshness, suggestions | `context-engine §4` | F4.5, F1.3 | Redaction; CAS on approval; INVALIDATE keeps history | Not started | | Port per §3 |

### F5 — Host API and input coordination

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F5.1 | `fugue serve`, `host.json`, `fugue.sock`, UID check, `Hello` and versioning | `host-api §3, §7, §8` | F0.7, F1.3 | Single host; socket permissions; versions | Not started | | |
| F5.2 | Event stream with sequence, snapshot, catch-up, `resync_required` | `host-api §6` | F5.1 | Disconnect and reconnect at the right sequence | Not started | | |
| F5.3 | Session, queue and interaction services over Connect | `host-api §4–5` | F5.2, F2.10 | `command_id` deduplication; `origin` derived from the principal | Not started | | |
| F5.4 | Input coordination: durable queue, Steer, Interject, Stop returns items to the composer, `Continue`, `Inject` | `coordination` | F5.3, F3.2 | Coordination evidence table §10, except peers | Not started | | Peers per Q-04 |
| F5.5 | Goal mode | `continuation §5` | F5.4, F6.1 | Goal evidence in continuation §8 | Blocked | | Waits for F6.1 |

### F6 — Tools and interaction

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F6.1 | Registry, descriptor, factory, result servant, binding revision | `tool-system §2–4, §10–11` | F2.10 | `ToolBindingRevised` only at step boundaries | Not started | | |
| F6.2 | Turn and tool snapshots wired into the harness | `tool-system §9` | F6.1, F1.7 | Restore touches only the tool's diff | Not started | | |
| F6.3 | Interaction owner API | H-24 | F6.1, F1.5 | — | Awaiting design | | |
| F6.4 | Sandbox runtime: per-session jail, process registry, environment snapshot | H-25 | F6.1, F1.6 | — | Awaiting design | | |
| F6.5 | File, shell and job, code reading, ask-user tools | H-17 to H-20 | F6.1–F6.4 | — | Awaiting design | | |

### F7 — Shell and UI

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F7.1 | Electron shell: package the Go binary, start and connect to the host, renderer security | H-44 | F5.1 | — | Awaiting design | | May be a separate repository |
| F7.2 | Native session UI: picker, presets, failure UX, jobs, snapshots | H-33 | F7.1 | — | Awaiting design | | |

### F8 — Stage A release

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F8.1 | Stage A release criteria | `activation` (needs rewriting) | F2–F7 | — | Needs decision | | Q-02 |

### After stage A

Import of DevKit data; MCP (`fugue-mcp`); the remaining DevKit modules (git, tasks, codegraph,
terminal, database, ssh, API client…). Each area needs its own design and plan before it enters
this board.

## 5. Proposed layout

Input to ADR F0.2, which is approved with the deviations it lists; where this sketch and
[ADR 0001](adr/0001-package-layout.md) differ, the ADR is authoritative.

```text
cmd/fugue/                 main: fugue serve and subcommands
internal/
  platform/                clock, ids, supervise, redact, errcode (stdlib only)
  config/                  typed configuration; the only reader of environment variables
  kernel/                  pure decide/evolve, commands, events, invariants (stdlib only, imports nothing of this module)
  codec/                   canonical codec v1
  crypto/  vault/          ported from DevKit
  storage/                 DB open, pragmas, migrations (DDL owner), data-directory lock, exec_host
  execstore/               SQL for the other exec_* tables, protected payloads
  coordinator/             per-session actor, dispatch, timers, scheduler
  contextengine/
  modeladapter/            neutral contract; anthropic/, openai/, faux/
  credentials/
  tools/                   registry, descriptors; concrete tools in subpackages
  interaction/  sandbox/  snapshot/  workspacememory/
  app/                     composition root and application services
  hostapi/                 Connect handlers; generated code in hostapi/internal/pb, importable only here
  arch/                    architecture tests
proto/fugue/<module>/v1/
tools/fuguevet/
quality/                   coverage and ratchet baselines
docs/                      roadmap, quality-gates, dependencies, ports, adr/
```

Dependency direction: `platform` ← `kernel` ← `coordinator` ← `app` ← `hostapi` ← `cmd`.
`execstore`, `modeladapter` and `tools` implement ports defined by `coordinator` and are wired
together only in `app`.

## 6. Gate progress

| Gate | Evidence | Items |
| --- | --- | --- |
| G-01 Package | ADR F0.2 approved; `TestLayerGraph` green | F0.2, F0.5 |
| G-02 Transition | Transition tables, invariants, property tests, fault injection | F2.1, F2.2, F3.5 |
| G-03 Storage | Atomicity, pragmas, triggers, crypto-shred, size limits, lock | F1.3, F2.4, F2.5 |
| G-04 Model and tool | Real adapters, real tools | F4.*, F6.* |
| G-05 Interaction | Owner API | F1.5, F6.3 |
| G-06, G-07 | Need rewriting for Fugue | Q-02 |

## 7. Needs decision

| ID | Date | Item | Question | Answer |
| --- | --- | --- | --- | --- |
| Q-01 | 2026-11-01 | F0.1 | Repository name and Go module path. Proposal: rename the repository to `fugue` (the target name in the Fugue naming ADR), module `github.com/syurodev/fugue`, unless a SynX GitHub organization exists. Which LICENSE? | Repository `fugue`, module `github.com/syurodev/fugue`; LICENSE Apache-2.0 (user, 2026-11-01) |
| Q-02 | 2026-11-01 | F8.1 | The activation design (H-36, H-37, G-06, G-07) assumes the native harness runs beside vendor sessions inside DevKit. Fugue is a new product: what does a stage A release require? | |
| Q-03 | 2026-11-01 | F1.3 | DB file name. The old ADR kept `devkit.db` only to avoid a migration; data now starts clean, so the proposal is `fugue.db` | |
| Q-04 | 2026-11-01 | F5.4 | Peer messages in the design go through the Agent Hub peer outbox, which this repository does not have. Proposal: drop peers from stage A; child sessions are already outside v1 | |
| Q-05 | 2026-11-01 | Design | Drop H-43 (inventory of DevKit frontend logic) and the `AppService` ratchet, since DevKit is frozen | |
| Q-06 | 2026-11-01 | F2.2 | `pgregory.net/rapid`, the property-test library named in quality-gates §6, is MPL-2.0, outside the allowed license list. Approve it, or name another library? | |
| Q-07 | 2026-11-01 | F0.3 | quality-gates §3 allows `panic` in `must*` helpers in `cmd`. `forbidigo` cannot scope by function name. Proposal: only files named `cmd/<name>/must.go` may call `panic`. Accept, or name another mechanism? | |
| Q-08 | 2026-11-01 | F0.4 | `secretlog` wording in quality-gates §4 is loose. Reading used: sinks are `slog`, `fmt`, `errors.New`/`Join` and test output (`t.Log`, `t.Error`, `t.Fatal`, `t.Skip`); a call into `internal/platform/redact` is the safe path; reported are `redact.Secret` values, byte slices or arrays named like key material (`key`, `dek`, `kek`, `nonce`, `salt`, `seed`) and any non-numeric value named like a secret (`token`, `secret`, `password`, `apikey`, `credential`, `privatekey`); numbers and booleans are exempt so token counts can be logged. Accept, or tighten (for example every `[]byte`)? | |

## 8. Log

| Date | Item | Change | Updated by |
| --- | --- | --- | --- |
| 2026-11-01 | — | Initial roadmap for the new repository | Designer |
| 2026-11-01 | F0.2 | ADR 0001 package layout approved with amendments (kernel imports nothing of this module; §5 sketch aligned); Q-06 added | Agent |
| 2026-11-01 | F0.3 | Toolchain, `.golangci.yml`, `Taskfile.yml`, `cmd/fuguegate`, gate self-tests and fixtures. golangci-lint pinned at v2.15.0 (v2.13.2 cannot type-check `reflect` on Go 1.27.2); `exhaustruct` is `exhaustruct_v5` (renamed upstream); `uniq-by-line: false` so no linter hides behind another; gremlins' threshold flag exits 0 at 0% so `fuguegate` reads its summary; Q-07 added | Agent |
| 2026-11-01 | F0.1 | Module `github.com/syurodev/fugue`, Apache-2.0 LICENSE, README; Q-01 answered | Agent |
| 2026-11-01 | F0.6 | Added `commit-lint` job and `task hooks` to the item (no `Co-Authored-By`, Conventional Commit PR titles; the item ID is optional); AGENTS.md rule and PR templates added | Agent |
| 2026-11-01 | F0.1–F0.3 | Marked Done after the merge of PRs #1, #2 and #4 | Agent |
| 2026-11-01 | F0.4 | `tools/fuguevet` with `kernelpure`, `nobarego`, `secretlog`, `todoref`, `noprotoleak` and the `vet` step. `kernelpure` also limits `fmt` to formatting functions and `time` to types, constants and methods; `noprotoleak` also covers the protobuf and Connect runtimes (ADR 0001 §6); Q-08 added | Agent |
