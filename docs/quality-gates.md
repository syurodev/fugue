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
| `todoref` | To-do comments use `TODO(FUG-<n>): ...` or `TODO(F<phase>.<n>): ...`, with an optional lowercase suffix on spec item IDs. The analyzer checks syntax and rejects `FIXME`, `HACK` and `XXX`. |
| `realclock` | Test files read no wall clock and time nothing: no `clock.Real`, `time.Now`, `time.Since`, `time.Until`, `testing.Benchmark` or `testing.B.Elapsed` outside `internal/perf` (the speed budgets, §6) and the `clock` package that tests its own real clock. A hang-only deadline goes through `testutil.Await` or `testutil.Poll`; a test that drives a real process gets real timers from `testutil.OSClock()`. No allowlist |
| `boundedwait` | A test loop that can only end when its condition holds (a bare `for`, a `for` with only a condition, or a counted or ranged loop that can `return`, `break` or `goto`) calls neither `runtime.Gosched` nor `time.Sleep`: such a wait has no deadline and, if the state never comes, spins until the package timeout kills every test in the package with one panic. The wait goes through `testutil.Await`, `testutil.Poll` or `testutil.Spin` (§6); `internal/testutil` itself is exempt. `t.Context()` and a context the test cancels at its end are not deadlines. A loop with no way out early is bounded work and may yield. No allowlist |
| `cheapvault` | Test files outside `internal/vault` and `internal/crypto` call neither `vault.Service.Setup` nor `crypto.NewKeyring` (three Argon2id derivations at 64 MiB and three passes each, which dominate run time under `-race`); a fixture takes the minimum-cost keyring from `internal/testutil/cheapvault`. No allowlist |
| `noprotoleak` | Packages outside `internal/hostapi`, `pkg/client` and `cmd/*` do not import code generated from proto, the protobuf runtime or Connect (ADR 0002, Q-31 3A) |

## 5. Architecture (`internal/arch`)

Ordinary Go tests that read the package graph with `go list -deps -json` and the AST.

| Test | Rule |
| --- | --- |
| `TestLayerGraph` | Every `internal/*` package has a row in the layer table (ADR F0.2); a reverse import or an undeclared new package fails |
| `TestCoreNeverImportsUI` | No package of the core module imports the `ui/` module (`github.com/syurodev/fugue/ui/...`) or the UI toolkit (`gioui.org/...`); the core stays cgo-free (DA-E06, ADR 0002) |
| `TestAppWritesOnlyThroughHostAPI` | Every package under `ui/` imports the core module only through `pkg/client` and `pkg/pb/...`; production files import no socket, SQLite or direct data-file API (`net`, `database/sql`, `modernc.org/*`, `os.Open*`/`os.Create`/`os.ReadFile`/`os.WriteFile`), except the direct file calls of the test-support package `ui/internal/shottest`, which only `_test.go` files may import (DA-E06, ADR 0002, FUG-36) |
| `TestTableOwnership` | `CREATE TABLE` appears only in `internal/storage` migrations; each table has exactly one owning package; only the owner has SQL touching that table; `database/sql` is imported only by an allowlist |
| `TestPackageDocs` | Every package has a `doc.go` whose package comment states purpose, owner and allowed callers |
| `TestGoleakEverywhere` | Every package with tests has a `TestMain` calling `goleak.VerifyTestMain` |
| `TestDependencyAllowlist` | Every module in `go.mod` has a row in `docs/dependencies.md` (reason, license, approver) |
| `TestErrorCodesPublished` | Every error code returned through the Host API is in the public registry or in the deliberately-hidden list with a reason |
| `TestVerificationEvidence` | In `docs/verification.md`, tests named as evidence for completed code gates exist and build for both Linux and macOS. Referenced probes and live verification records are checked. |
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
| A flaky test is a bug: no quarantine, no automatic retry, no `t.Skip` | PR stress and nightly repeats (below); the policy below |
| Tests do not measure time: the fake clock drives time, a hang-only deadline is a generous fixed `testutil.Await` or `testutil.Poll`, and a speed or speed-ratio assertion lives in `internal/perf` | `realclock` (§4) |
| No loop in a test waits with `runtime.Gosched` or `time.Sleep` and no deadline: a wait is `testutil.Await` (a signal), `testutil.Poll` (state outside the process) or `testutil.Spin` (state inside it, such as a fake-clock timer), each of which fails the one test after a generous hang timeout instead of spinning until the package timeout. `t.Context()` is not a deadline. A loop that cannot leave early (a fixed count with no `return` or `break`) is bounded work and may yield | `boundedwait` (§4); `TestBoundedwaitAnalyzerFlagsUnboundedWaitLoops` |
| Per-test time budget: no test runs longer than 30 s under `-race` on a PR runner. The PR `test` job runs `go test -json`, then `fuguegate slowtests -json test.json -max 30s` lists every test above the budget; the step only reports (a warning, never a failed job) until the owner makes it required. A test over the budget is made smaller (keeping the property it checks) or its full-size variant moves to the nightly `perf` job under the `perf` build tag, as `TestTranscriptPageIsStableAcrossPagesWithoutDuplicatesOrGaps` (637 items; 10,000 in the nightly) did | `fuguegate slowtests`; `TestSlowtestsReportsEveryTestAboveTheBudget` |
| Speed budgets run only in the nightly `perf` job, as the median of several measurements against each budget with every number in the log; PR CI checks correctness only | Tests under the `perf` build tag (`internal/perf`, and the ui layout budgets); `TestPerfBudgetsRunOnlyInNightly` |
| A PR that changes test files reruns the test functions those files declare, in their packages, with `-race -shuffle=on`, ten separate `-count=1` runs, on ubuntu, and on macOS when the platform filter is true | `stress` job; `fuguegate changes` prints `test_pkgs` and `test_run`; `TestPRStressRerunsChangedTestPackages` |
| Nightly: every package, `-race -shuffle=on`, twenty repetitions on ubuntu and macOS; each test that both passed and failed is reported | `repeat-tests` job, `task nightly-test`, `fuguegate flaky`; `TestNightlyStressRunsTwentyCountsOnUbuntuAndMacOS` |
| Fixtures use the cheap vault and the shared helpers (`internal/testutil`), never production-cost vault setup or an unsafe fake-executable writer | `TestEveryFixtureUsesTheCheapVault`, `TestFakeExecutableWriterHoldsTheForkLock` |
| OS-specific tests (sandbox) run on the right OS | CI matrix of macOS and Linux |
| Only stdlib `testing` and `github.com/google/go-cmp` for comparisons | `depguard` |

### Flaky tests

A flaky test is a bug in the test or in the code, not a property of the runner.

- A test that needed a CI rerun is fixed within a week of the first failure, or the commit that
  made it flaky is reverted. The first failure is logged in the "known flaky" list in
  [§11](#known-flaky-tests) the day it is seen, and the entry stays until the fix merges.
- Still no `t.Skip`, no quarantine and no automatic retry: a rerun is how a human learns that a
  test is flaky, never a way to turn a red check green.
- The nightly stress run keeps the PR package timeout of 10 minutes. The timeout covers all
  counts of one package binary, so the nightly repeats `go test -count=1` twenty times (a fresh
  shuffle each time) instead of one `-count=20`; a package that needs more than 10 minutes for
  one repetition is made faster or split, and the timeout is never raised.
- A local gate needs no network. `task proto` reaches the buf schema registry for the pinned
  remote TypeScript plugin; see [§11](#11-gate-change-log) (2026-11-16) for what runs offline.

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
  `coverage` and `fuzz-short`. The other gates always run, including `arch`, which reads the
  verification registry and ADRs.
- Linux tests run for every code change; macOS tests run when the change is platform-specific:
  a build constraint, an OS-suffixed file name, or a path under `cmd/`, `internal/process/`,
  `internal/hostapi/`, `internal/storage/`, `internal/sandbox/`, `internal/snapshot/` or
  `.github/workflows/`, or `go.mod`, `go.sum`, `Taskfile.yml`;
- the macOS entries of `test`, `stress` and the `ui` gates are separate jobs gated by `if` at the
  job level (2026-11-18), not matrix entries decided by a step: a skipped job requests no runner,
  where a skipped step still boots one first, and GitHub cancels a job that queues without
  acquiring a hosted runner after roughly 15 minutes with zero steps. A skipped job without a
  matrix reports under its literal name (`test (macos-latest)`) and a skipped check counts as
  success for required status checks, so every required check still reports on every PR;
- every push to `main` runs the macOS tests (`main.yml`);
- **PR tests paused (2026-11-23):** `test`, `stress` and `coverage` (both systems) are skipped on
  PRs unless the repository variable `PR_TESTS` is `on`; the skipped jobs still report under their
  names, so the required checks pass. Every pushed `v*` tag runs them in `build.yml`: the full
  suite on ubuntu and macOS, coverage, and ten `-count=1` stress runs of the test functions changed
  since the previous `v*` tag (`fuguegate changes`); a red tag build is not a release;
- the `ui/` Go module has its own workflow (`ui.yml`, ADR 0002): build, vet, lint and tests with `-race` on ubuntu-latest (Wayland/X11 dev packages) and macos-latest, triggered when the change set touches `ui/`, `pkg/` or `proto/` (the `changes` job prints `ui=` alongside `code=` and `platform=`).

**Stress (PR, new 2026-11-16):** the `stress` job reruns the `Test` and `Fuzz` functions declared in
the test files that changed (`-run '^(A|B)$'`, built by `fuguegate changes` as `test_run`) in their
packages with `-race -shuffle=on`, as ten separate `-count=1` runs (a fresh shuffle each), on ubuntu and, when `platform` is true, on macOS. It
reports success when no test file with a test function changed, like the other conditional jobs.
Why not the whole package: the 10-minute package timeout covers all ten counts, and `internal/app`
alone takes 200 to 390 seconds per count, so a PR touching its tests always timed out (PR #152's own
run). Rerunning only the changed functions is not enough alone: a PR that rewrites many `internal/app`
tests still passed the timeout on macOS with one `-count=10` run. So each repetition is its own `go test`
invocation under its own 10-minute timeout, like the nightly: a run exceeds the ceiling only if one
count of the selected tests does. A flaky test the PR
adds or edits is exactly what the job exists to catch. A helper-only change (a test file with no test
function) is not stressed; the regular test job still runs the package once, and the nightly run
repeats everything. The timeout stays at 10 minutes. The stress jobs report only: they are
not required status checks (owner decision, 2026-11-16). The `ui/` module is not stress-run (known gap).

**Nightly:** long fuzzing, full mutation, every test repeated twenty times on ubuntu and macOS
(`repeat-tests`, with a report of every test that both passed and failed), the `perf` speed
budgets, the upgrade and downgrade `drill` (A-E08), Windows build.

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
| 2026-11-08 | F10.2a (core side): `noprotoleak` allows `pkg/client` and `cmd/*` to import the generated code (now `pkg/pb`), protobuf and Connect, and still guards every other `internal/` package; `nobarego` exempts the test files of `pkg/`; `TestLayerGraph` treats `pkg/` rows as ordinary rows (edges via the May import column, `pkg/` never imports `internal/`); the coverage floor extended to `pkg/*` (generated `pkg/pb` excluded, 80% default floor, baseline entry for `pkg/client`); `fuguegate changes` gains the `ui` scope | Generated code must be importable by the public client and the UI module (Q-31 3A); `pkg/client` is shipped API and gets the same floor as internal packages. Guards: `TestLeaks`, `TestAllowedImporters`, `TestNoProtoLeak`, `TestLayerGraph`, `TestChangeScope` | user (`gate-change-approved` on this PR) |
| 2026-11-15 | `ui.yml`: the ubuntu test step installs `xvfb` and runs under `xvfb-run -a` with `LIBGL_ALWAYS_SOFTWARE=1` (macOS runs unchanged on Metal) | The F10.8 icon evidence test renders through gio's headless window, which needs an EGL context; the GPU-less ubuntu runner failed with `eglInitialize failed: 0x3001`. No threshold moved and no gate changed; guards: `TestUIWorkflowRunsTheModuleGates`, `TestActionsArePinnedToCommits` | user (`gate-change-approved` on PR #135) |
| 2026-11-08 | F10.2a (ui module): the two DA-E06 tests `TestCoreNeverImportsUI` and `TestAppWritesOnlyThroughHostAPI` added; `ui.yml` workflow runs the `ui/` module gates (§9); `gioui.org` and `golang.org/x/image` added to `docs/dependencies.md` | Core never imports UI and UI only imports pkg/client and pkg/pb (ADR 0002, DA-E06); the UI module needs its own CI workflow for its cgo build. Guards: `TestCoreNeverImportsUI`, `TestAppWritesOnlyThroughHostAPI`, `TestUIWorkflowRunsTheModuleGates`, `TestActionsArePinnedToCommits` | user (`gate-change-approved` on this PR) |
| 2026-11-17 | F0.10 test speed and stability: new `boundedwait` analyzer (§4) and `testutil.Spin` / `testutil.WaitFor`; `fuguegate slowtests` and a report-only per-test budget of 30 s in the PR `test` job, which now runs `go test -json` through `task test -- -json` (§6); `task perf` also runs the full-size variants of two tests that are smaller in the PR run | The nightly was red five days in a row because `internal/app` hit the 10-minute package timeout, and a hung wait in a test spun until that timeout instead of failing one named test. No threshold moved and nothing was loosened: every wait now has a hang timeout, the full-size variants of the two heaviest tests still run nightly, and the budget only reports. Guards: `TestBoundedwaitAnalyzerFlagsUnboundedWaitLoops`, `TestSlowtestsReportsEveryTestAboveTheBudget` | user (chat request; `gate-change-approved` on this PR) |
| 2026-11-16 | F0.9 flake hardening: new `realclock` analyzer (§4); `internal/testutil` and `internal/perf` packages with a `support` and a `perf` layer row (ADR 0001) and `TestLayerGraph` rejecting production imports of a `support` package; `cmd/fuguegate` tests may import `internal/testutil`; `stress` PR job and a twenty-repeat nightly on ubuntu and macOS with `fuguegate flaky` (§6, §9); speed budgets move to the nightly `perf` job; flaky-test policy (§6). The nightly repeats `go test -count=1` twenty times, not `-count=20`, because the 10-minute timeout covers all counts of one package binary and `internal/app` alone takes minutes per count; the timeout is unchanged | Five classes of flaky tests failed CI on a different test on each rerun on 2026-11-15 and 2026-11-16: wall-clock timing assertions, expensive setup under `-race`, OS races on a fake binary and `killpg`, parallel contention near the package timeout, network in a local gate. No threshold moved, no ratchet baseline changed and nothing was loosened: tests that asserted speed in PR runs are now checked as a median in the nightly `perf` job, and the PR gains the stress job. Guards: `TestRealclockAnalyzerFlagsWallClockInTests`, `TestPRStressRerunsChangedTestPackages`, `TestNightlyStressRunsTwentyCountsOnUbuntuAndMacOS`, `TestPerfBudgetsRunOnlyInNightly`, `TestLayerGraph` | user (chat request; `gate-change-approved` on this PR) |
| 2026-11-18 | CI scope: the macOS entries of the `test`, `stress` and `ui` gates become separate jobs gated by `if` at the job level instead of matrix entries decided by a step (§9). Job names, scope rules, required checks and every real macOS run are unchanged (`main.yml` still tests every merge) | On PRs #155, #160 and #161, `test (macos-latest)`, `stress (macos-latest)` and `ui (macos-latest)` were cancelled after exactly 15 m 01 s with zero steps: the step-level decision still requested a macOS runner on every PR, and GitHub cancels a job that queues without acquiring a hosted runner after roughly 15 minutes. A skipped job requests no runner and reports under its literal name; a skipped check counts as success for required status checks. Guards: `TestPRWorkflowRunsEveryRequiredJob`, `TestPRStressRerunsChangedTestPackages`, `TestUIWorkflowRunsTheModuleGates` | user (chat request; `gate-change-approved` on this PR) |
| 2026-11-23 | `TestAppWritesOnlyThroughHostAPI`: the package `ui/internal/shottest` (golden shot comparison, test support) may call `os.ReadFile` and `os.WriteFile`; the same test now fails any production `ui` file that imports it | The shot comparator reads golden PNGs and writes failing renders; it is test support, and the import rule keeps the app from ever reaching those calls. Fixtures: `ui-writes-good` (the exempt package, a test importing it) and `ui-writes-bad` (a production importer, a lookalike package with no exemption) | user (`gate-change-approved` on the FUG-36 PR) |
| 2026-11-22 | Retire the Markdown roadmap integrity and evidence tests. Validate Linear and spec ID syntax in `todoref` (§4–5) | Work tracking moved to Linear. `TestVerificationEvidence` checks release verification. `TestTodoRef` and `TestVettoolRunsEveryAnalyzer` exercise the analyzer. | user (chat request and `gate-change-approved` on this PR) |
| 2026-11-23 | FUG-44: new nightly job `drill` and `task drill` run the upgrade and downgrade drills of A-E08: `TestDrillAcrossBuilds` (behind the `drill` build tag) builds an old `cmd/fugue` test binary from the merge-base, or from the parent of the last commit that changed migrations or proto, kills it mid-run, resumes with the HEAD build and requires the old build to refuse the newer data directory; `TestUpgradeDrillSameBuildResumesRecovery` and `TestDowngradeDrillRefusesNewerSchemaWithoutAppending` run the same drill with one build and a synthetic newer schema version. Guard: `TestDrillRunsOnlyInNightly` | Nothing in the suite built two binaries, so no test could show that an old build leaves a newer data directory alone. No threshold moved and nothing was loosened. | pending (`gate-change-approved` on this PR) |
| 2026-11-23 | PR tests **paused**: the `pr.yml` jobs `test`, `stress` and `coverage` on ubuntu and macOS run only when the repository variable `PR_TESTS` is `on`; new `build.yml` runs them on every pushed `v*` tag (full suite and coverage on both systems, stress of the test functions changed since the previous tag) and by hand (`workflow_dispatch`). `main.yml` (macOS tests on every merge), the nightly twenty-repeat run and every other PR gate are unchanged | Speed up merging for the stage A push: the test and stress jobs are the slowest PR jobs (the macOS `internal/app` run alone hit the 10-minute timeout, FUG-53). Temporary: re-enable with `gh variable set PR_TESTS --body on` (no workflow change), at the latest before `task release-check` closes stage A. A red `main.yml`, nightly or tag build is fixed like a broken build. Guard: `TestTagBuildRunsTestsWhilePRTestsArePaused`. Same day: the `main` ruleset no longer requires a branch to be up to date with `main` before merging (`docs/github-setup.md` §3), because every merge forced each open PR to rebase and rerun its checks | user (chat request; `gate-change-approved` on this PR) |

### Known flaky tests

A test that needed a CI rerun is listed here the day it fails and removed by the PR that fixes
it; the fix is due within a week (§6).

| First failed | Test | Failure | Fix |
| --- | --- | --- | --- |
