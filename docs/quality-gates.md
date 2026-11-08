# Quality gates

This page defines every code-quality rule in the repository and **how a machine enforces** each
one. A rule without an enforcement mechanism is not a rule yet: either add the mechanism or drop
the rule.

General principles:

1. **Gates come before code.** Every gate on this page is switched on in phase F0, on a nearly
   empty repository. There is no "enable gradually" phase and no `only-new-issues`.
2. **Every gate has a negative test.** Each gate has a deliberately violating fixture and a test
   proving the gate catches it. A gate that misses its violation counts as broken.
3. **Tighten only, never loosen.** Thresholds and baselines move only toward stricter. Loosening
   anything requires the user's decision (see [§10](#10-changing-a-gate)).
4. **Local matches CI.** `task check` runs exactly the PR CI gates, except long jobs that run
   nightly only.

## 1. Formatting

| Rule | Enforcement |
| --- | --- |
| Code is formatted with `gofumpt` and `goimports` (import groups: stdlib, third party, this module) | golangci-lint formatters; CI fails if `task fmt` produces a diff |
| Generated code (proto, mocks if any) is never edited by hand | The `generate` job reruns generators and fails on any diff |

## 2. Lint (golangci-lint v2)

`.golangci.yml` uses `default: none` and enables an explicit list; **test files are linted**; no
`only-new-issues`; `max-issues-per-linter: 0`, `max-same-issues: 0`.

| Group | Linters | Notes |
| --- | --- | --- |
| Correctness | `govet` (every analyzer except `fieldalignment`), `staticcheck` (all checks), `errcheck` (including `check-blank`, `check-type-assertions`), `ineffassign`, `nilerr`, `nilnil`, `bodyclose`, `sqlclosecheck`, `rowserrcheck`, `contextcheck`, `noctx`, `copyloopvar`, `durationcheck`, `makezero`, `reassign`, `recvcheck` | `errcheck` is fully on; a `Close` error on a write path must be handled, read paths use a clearly named `closeQuietly` helper |
| Errors | `errorlint`, `err113`, `wrapcheck` (package boundaries only; the sanctioned unwrapped conversions are `errcode` constructors and `connect.NewError(`) | No dynamic `errors.New` inside functions; use sentinels or typed errors with codes |
| Enums and structs | `exhaustive` (`default-signifies-exhaustive: false`), `exhaustruct` (only for `kernel` and `hostapi` types whose names end in `Command`, `Event`, `Request`) | Adding an event and forgetting a `switch` branch fails |
| Complexity | `cyclop` (12), `gocognit` (15), `nestif` (4), `funlen` (60 lines, 40 statements), `maintidx` | No per-function threshold bumps; split the function |
| Junk code | `unused`, `unparam`, `dupl` (100 tokens), `goconst`, `gocritic` (with `commentedOutCode`, `deferInLoop`, `hugeParam`, `rangeValCopy`), `unconvert`, `wastedassign`, `prealloc`, `intrange`, `usestdlibvars`, `perfsprint` | Commented-out code is caught by `commentedOutCode` |
| Global state | `gochecknoglobals`, `gochecknoinits` | Only exceptions: error sentinels and read-only constant tables, listed in the linter allowlist |
| Concurrency | `containedctx`, `fatcontext` | `context.Context` is the first parameter and is never stored in a struct |
| Security | `gosec` (all rules; each exception needs a reason), `forbidigo` (table in §3) | |
| Tests | `paralleltest`, `tparallel`, `thelper`, `usetesting`, `testableexamples` | Tests use `t.Context()`, `t.TempDir()`, `t.Setenv()` |
| Docs and comments | `revive` (`package-comments`, `exported`, `var-naming`, `receiver-naming`, `indent-error-flow`, `early-return`, `unexported-return`), `godot`, `misspell` | Every package comment states **purpose and owner** |
| `nolint` discipline | `nolintlint` (`require-explanation`, `require-specific`, `allow-unused: false`) | The number of `nolint` directives is ratcheted (§8) |
| Imports | `depguard` | Bans `testify`, `pkg/errors`, `log`, third-party loggers; new modules go through [§7](#7-dependencies) |

## 3. Forbidden calls (`forbidigo`)

| Pattern | Forbidden in | Use instead |
| --- | --- | --- |
| `time.Now`, `time.Since`, `time.Until`, `time.Sleep`, `time.After`, `time.Tick`, `time.NewTimer`, `time.NewTicker` | Everywhere except `internal/platform/clock` | Injected `clock.Clock`; tests use the fake clock |
| `math/rand`, direct `crypto/rand.Read` | Everywhere except `internal/platform/ids` and `internal/crypto` | Injected ID and randomness ports |
| `os.Getenv`, `os.LookupEnv` | Everywhere except `internal/config` | Explicit config structs |
| `fmt.Print*`, `log.*`, `println` | Everywhere except `cmd/*/main.go` | `log/slog` with a redacting handler |
| `panic(` | Non-test code, except `must*` helpers in `cmd` | Typed errors |
| `t.Skip`, `testing.Short` | Tests | Make the test runnable or delete it; OS-specific tests use build tags |
| `context.Background`, `context.TODO` | Everywhere except `cmd/*/main.go` and tests | Context passed in by the caller |
| `reflect.` | `internal/kernel` | Explicit type switches |

## 4. Custom analyzers (`tools/fuguevet`)

Written with `golang.org/x/tools/go/analysis` and run through `go vet -vettool`. Each analyzer
has `analysistest` cases with valid and violating fixtures.

| Analyzer | Rule |
| --- | --- |
| `kernelpure` | `internal/kernel` imports only allowlisted stdlib (no `os`, `io`, `net`, `sync`, `time` beyond the `time.Duration`/`time.Time` types, `runtime`, `reflect`, `unsafe`); no writable package variables; no `go` statements |
| `nobarego` | `go` statements are allowed only in `internal/platform/supervise`; everything else calls `supervise.Go(ctx, name, fn)` so every goroutine has an owner, a name and a stop path |
| `secretlog` | Values of type `redact.Secret`, `[]byte` key material, or fields and variables whose names match `(?i)token\|secret\|password\|apikey\|credential\|privatekey` must not reach `slog`, `fmt`, `errors` or error messages directly; only through `redact` |
| `todoref` | `TODO`, `FIXME`, `HACK`, `XXX` are allowed only as `TODO(F<phase>.<n>): ...` with an ID present in `docs/roadmap.md` and not yet `Done` |
| `noprotoleak` | Packages outside `internal/hostapi` do not import code generated from proto |

## 5. Architecture (`internal/arch`)

Ordinary Go tests that read the package graph with `go list -deps -json` and the AST.

| Test | Rule |
| --- | --- |
| `TestLayerGraph` | Every `internal/*` package has a row in the layer table (ADR F0.2); a reverse import or an undeclared new package fails |
| `TestTableOwnership` | `CREATE TABLE` appears only in `internal/storage` migrations; each table has exactly one owning package; only the owner has SQL touching that table; `database/sql` is imported only by an allowlist |
| `TestPackageDocs` | Every package has a `doc.go` whose package comment states purpose, owner and allowed callers |
| `TestGoleakEverywhere` | Every package with tests has a `TestMain` calling `goleak.VerifyTestMain` |
| `TestDependencyAllowlist` | Every module in `go.mod` has a row in `docs/dependencies.md` (reason, license, approver) |
| `TestErrorCodesPublished` | Every error code returned through the Host API is in the public registry or in the deliberately-hidden list with a reason |
| `TestRoadmapEvidence` | Every test name in the Evidence column of a `Done` row in `docs/roadmap.md` exists in code and is not excluded from CI by a build tag |
| `TestFileSize` | Non-test files at most 500 lines; test files at most 800 lines; exceptions only through the ratchet |
| `TestNoPlaceholderCode` | No function that only returns `nil, nil`, `errors.New("not implemented")` or has an empty body, except no-op interface implementations named `Nop*` |
| `TestNewDeterministicIsTestOnly` | `ids.NewDeterministic` appears only in test files; production uses `ids.CryptoRandom` (the deterministic stream is not random and must never feed a key) |

## 6. Tests

| Rule | Enforcement |
| --- | --- |
| All tests run with `-race -shuffle=on -count=1` | `test` job |
| No `time.Sleep` in tests; time goes through the fake clock | `forbidigo` |
| Statement coverage: `kernel`, `codec`, `execstore`, `coordinator`, `crypto`, `vault` **≥ 90%**; other `internal/*` packages **≥ 80%**; `cmd` and generated code excluded | Per-package `quality/coverage.json`; a drop of more than 0.5 points below baseline fails |
| Mutation score (`gremlins`) **≥ 85%** for `kernel`, `codec`, `execstore`, `coordinator` guards, `crypto`, `vault` | PR: changed packages, **paused since 2026-11-05** (§11); discovery and each package have a separate 20-minute default timebox; nightly: everything with the same per-package timebox |
| Property-based tests (`pgregory.net/rapid`) for the kernel: random command sequences, invariant checker after every batch, replaying twice yields the same state | Required test of F2.2 |
| Fuzzing for every decoder and parser: codec, payload envelope, provider stream converters, path validation | PR: 20 s per target; nightly: 10 minutes; the `testdata/fuzz` corpus runs as ordinary tests |
| Golden files for traces and provider streams, updated with `-update`; a golden change needs a reason in the PR | Review; the `generate` job blocks stale golden files |
| Fault injection: the store has named, injectable crash points | F3 tests |
| A flaky test is a bug: no quarantine, no automatic retry | Nightly `-count=5`; an unstable test is fixed or the commit that caused it is reverted |
| OS-specific tests (sandbox) run on the right OS | CI matrix of macOS and Linux |
| Only stdlib `testing` and `github.com/google/go-cmp` for comparisons | `depguard` |

The mutation gate's `-budget` (default 20 minutes) applies separately to discovery/tool
preparation and each selected package. Packages run sequentially and every package must
meet the 85% score; a failure still stops the gate. A three-package run can therefore take
up to 60 minutes for mutation plus discovery. Caller cancellation still stops the run.

### Local resource defaults

`task test`, `task check`, fuzz, mutation and other Task commands automatically use
`GOMAXPROCS=2`, `GOFLAGS=-p=1` and `GREMLINS_UNLEASH_WORKERS=1` on local machines.
This limits Go execution to two processors per process, package build/test concurrency to
one, and mutation workers to one. Test and fuzz parallelism defaults to `GOMAXPROCS`.
These settings reduce aggregate CPU pressure; they are not a hard CPU quota across all
child processes. Runs can take longer, especially mutation tests within the existing time box.

A nonempty `CI` environment variable disables these defaults, leaving runner settings
unchanged. Explicit shell values for each setting are preserved; an existing `GOFLAGS`
value replaces the default `-p=1`, so include `-p=1` there to retain that limit.
The same tests, race/shuffle/count flags, fuzz duration, mutation selection and thresholds
still apply locally and in CI. No per-invocation configuration is required.

The heavy tasks (`test`, `test-pkg`, `coverage`, `fuzz-short`, `mutation-changed`,
`mutation`, `lint`, `vet`) also run at low local priority through the `HEAVY_WRAP` task
variable (`taskpolicy -c utility` on macOS, `nice -n 10` on Linux, inherited by every
child process) and behind the machine-wide heavy lock through the `HEAVY_LOCK` variable
(`go run ./cmd/fuguegate exclusive --`): at most one heavy task runs per machine at a
time, so heavy work from several worktrees queues instead of swapping the machine. Both
variables are empty under `CI`, leaving runner settings untouched, and on other
platforms, which have no equivalent in the base install. The wrappers sit at exactly one
level — on the heavy tasks' own commands, never on `check`, whose composed tasks would
nest the same flock and deadlock. `task arch` stays unwrapped on purpose: it is a single
light package. Measured on an 11-core, 18 GB macOS machine under a saturating bare
`go test -race ./internal/...` (peak single-process RSS 3.2-4.4 GB): the priority wrap
left the gate's wall time unchanged within run-to-run noise and shortened a foreground
stall about 10 %; `-b` (background) measured the same with the added risk of background
I/O throttling inside the mutation time box, so `utility` is the setting. `quality/`
pins both expansions and that every heavy task carries both wrappers while no other
task does.

Targeted runs use `task test-pkg PKG=<pattern> RUN=<regex> [COUNT=<n>]`: exactly the
`task test` flags (`-race -shuffle=on -count=1`) with the same local limits, wrappers
included, but no cover profile (a partial profile would fail the coverage baselines).
A bare `go test` is never used — `AGENTS.md` requires `task test-pkg` or `task arch`.

Heavy commands no task covers go through the machine-wide heavy lock directly:
`go run ./cmd/fuguegate exclusive -- <cmd...>`. It takes an exclusive `flock` on
`os.UserCacheDir()/fugue/heavy.lock` (created on first use through an `os.Root`, never
unlinked), logs once that it is waiting when another process holds the lock, retries on
the injected clock until the holder dies, then runs the command with inherited stdio,
propagating its exit code (128+signal for a signal death) and forwarding Interrupt and
Term to it. The kernel releases the flock when the process dies, so a crash never
leaves the machine queued. `exclusive` fails closed on platforms without `flock`; CI
behavior is unchanged either way.

## 7. Dependencies

| Rule | Enforcement |
| --- | --- |
| Every module has a reason, a license (MIT, BSD, Apache-2.0, ISC; anything else needs user approval) and an approver | `TestDependencyAllowlist`, `go-licenses check` |
| `go.mod` is tidy and verified | `go mod tidy -diff`, `go mod verify` |
| No known vulnerability on a reachable path | `govulncheck ./...` |
| Dev tools are version-pinned | `tool` directives in `go.mod`; golangci-lint version pinned in CI and the Taskfile |
| Toolchain is pinned | `go` and `toolchain` in `go.mod` |

## 8. Dead code and ratchets

| Rule | Enforcement |
| --- | --- |
| No function unreachable from `cmd/*` or tests | `deadcode -test ./...` must be empty |
| No exported symbol used only inside its own package | `unused` with `exported-fields-are-used: false`, plus review |
| The counts of `nolint` directives, file-size exceptions and `gosec` exceptions never grow | `quality/ratchet.json`; when a count improves the PR must lower the baseline |

## 9. CI

**PR (must pass to merge):** `fmt`, `lint`, `vet` (including `fuguevet`), `arch`, `test` (macOS
and Linux), `coverage`, `deadcode`, `mod` (tidy, verify, govulncheck, licenses), `proto`
(`buf lint`, `buf breaking` against `main`, generate without diff), `fuzz-short`,
`mutation-changed` (paused since 2026-11-05: the job reports success without running, see §11), `gate-guard`.

**Scope (2026-11-08).** `pr.yml` runs the code gates on pushes only (`opened`, `synchronize`,
`reopened`); `gate-guard` and `commit-lint` live in `pr-meta.yml`, which also reacts to labels
and title edits, so applying a label reruns only those two. A `changes` job
(`fuguegate changes`) reads the change set and fails safe (an unreadable diff runs everything):

- documentation-only changes (Markdown outside `testdata`, except this file) skip `test`,
  `coverage` and `fuzz-short`; the other gates, including `arch`, which reads the roadmap and the
  ADRs, always run;
- Linux tests run for every code change; macOS tests run when the change is platform-specific:
  a build constraint, an OS-suffixed file name, or a path under `cmd/`, `internal/process/`,
  `internal/hostapi/`, `internal/storage/`, `internal/sandbox/`, `internal/snapshot/` or
  `.github/workflows/`, or `go.mod`, `go.sum`, `Taskfile.yml`;
- skipped steps leave the job green, so every required check still reports;
- every push to `main` runs the macOS tests (`main.yml`).

**Nightly:** long fuzzing, full mutation, tests with `-count=5`, Windows build.

`main` has branch protection: the checks above are required, squash merge only, no direct push
and no force push.

## 10. Changing a gate

Gate-defining files: `.golangci.yml`, `tools/fuguevet/**`, `internal/arch/**`, `quality/**`,
`docs/quality-gates.md`, `docs/dependencies.md`, `.github/workflows/**`, `Taskfile.yml`.

- `CODEOWNERS` assigns these files to the user.
- The `gate-guard` job fails when a PR touches these files without the `gate-change-approved`
  label. Only the user applies that label.
- Tightening (enabling a linter, lowering a threshold, lowering a baseline) still needs the
  label but no long justification. Loosening anything needs a row in
  [§11](#11-gate-change-log).
- Agents never edit generated code, baselines or fixtures to get past a gate.

## 11. Gate change log

| Date | Change | Reason | Approver |
| --- | --- | --- | --- |
| 2026-11-01 | Initial version | — | user |
| 2026-11-01 | `depguard`: `log/slog` is allowed explicitly (the ban on `log` matched it by prefix) | Correction: the gate bans `log`, not `log/slog`; `lint:ok depguard` fixture added | user (label on the F0.8 PR) |
| 2026-11-01 | `wrapcheck`: `internal/platform/errcode` is ignored as a package boundary | Its constructors are how the repository wraps errors; flagging them would force a wrapper around every typed error. Narrow exception, fixture for `wrapcheck` unchanged | user (label on the F0.8 PR) |
| 2026-11-02 | `go-licenses check`: `--ignore=modernc.org/mathutil` | `modernc.org/mathutil@v1.7.1` is BSD-3-Clause (`Copyright (c) 2014 The mathutil Authors`), but its third clause words "the authors" instead of "the copyright holder", scoring ~0.88 in `google/licenseclassifier` below the 0.90 default; `--ignore` still checks its dependencies (`bigfft`) | user (label on the F1.3 PR) |
| 2026-11-05 | Mutation budget: 20 minutes per package instead of one shared 20-minute deadline | PR #52 completed crypto at 98.1% and execstore at 88.4%, leaving vault only 6.9 seconds. Vault passes separately at 96.1%. Give discovery and each package their own budget; preserve the 85% threshold, selected packages, per-mutant timeout coefficient and CPU limits. Regression: `TestMutationPackageBudgetDoesNotCancelNextPackage` | user (chat approval; owner-applied `gate-change-approved` on PR #52) |
| 2026-11-05 | Mutation on changed packages **paused**: the PR job `mutation-changed` reports success without running, and `task check` no longer runs it. Threshold (85 %), package selection, budgets and the nightly `mutation-full` job are unchanged; `task mutation-changed` still runs by hand | It made every PR and local check too slow for the current pace of work. Temporary: re-enable by restoring the job step and the `task check` step together, at the latest before F8.1 (`task release-check`). Until then, a nightly `mutation-full` failure is fixed like a broken build. Guard: `TestTaskfileRunsEveryGate` requires the task to exist and the pause to be explicit | user (chat request) |
| 2026-11-06 | Local heavy tasks run at low priority (`HEAVY_WRAP`: `taskpolicy -c utility` on macOS, `nice -n 10` on Linux) and behind the machine-wide heavy lock (`HEAVY_LOCK`: `fuguegate exclusive`), both empty under `CI`; `task test-pkg` added for targeted runs; `docs/plans` commands use `task test-pkg` instead of bare `go test` | Several agents running gates in separate worktrees swapped and froze an 18 GB machine: a bare `go test` ran 11 race binaries in parallel at about 1 GB each, and concurrent `task check` runs had no machine-wide cap. Measured: a saturating bare race run peaks at 3.2-4.4 GB RSS; under it the `utility` wrap kept gate wall time unchanged and cut a foreground stall about 10 %, `-b` measured the same with added throttling risk, so `utility` won. Concurrent `task check` pairs queue one heavy task at a time; the numbers are in the PR description. No threshold moved and no ratchet baseline changed; guards: `TestHeavyTasksRunWrappedAndQueued`, `TestHeavyWrapExpansion` | user (`gate-change-approved` on this PR) |
| 2026-11-07 | `wrapcheck`: `internal/hostapi/listener.go` is exempt from wrapping by a path-scoped exclusion rule | The transport must return accept and connection-close errors unchanged: `http.Server.Serve` recovers from a temporary `net.Error` by type assertion and detects `net.ErrClosed`; wrapping turned a recoverable accept failure (EMFILE) into a fatal `Serve` exit. The file is the only place that passes `net.Listener`/`net.Conn` errors through. Regression: `TestAcceptReturnsTheInnerErrorUnchanged` | user (`gate-change-approved` on PR #63) |
| 2026-11-08 | CI scope: documentation-only PRs skip `test`, `coverage` and `fuzz-short`; macOS tests run on PRs only for platform-specific changes and on every push to `main` (`main.yml`); `gate-guard` and `commit-lint` move to `pr-meta.yml` so labels and title edits no longer rerun the code gates (§9) | The macOS test job (5–6 min) was the longest PR job and ran even for roadmap-only PRs, and every label or title edit cancelled and restarted the whole run. No threshold moved and no gate was removed: Linux tests still run for every code change, macOS still runs on every merge, and the change detector fails safe. Guards: `TestChangeScope`, `TestChangesFailsSafeOnABrokenDiff`, `TestPRWorkflowsSplitByTrigger` | user (chat request; `gate-change-approved` on this PR) |
