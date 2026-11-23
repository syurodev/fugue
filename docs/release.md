# Stage A release

How to cut a release candidate (RC) and run the stage A release gate. Design: `activation §5–6`
and `verification §6–7`. The registry is [verification.md](verification.md); the gates are in
[quality-gates.md](quality-gates.md).

## What the gate checks

`task release-check` on the RC, on Linux and on macOS:

1. `check`: every PR gate.
2. `probes`, the full `mutation` and `drill` (the A-E08 upgrade and downgrade drills): the nightly
   gates, run on the RC.
3. `release-evidence`:
   - `TestReleaseEvidence` (build tag `release`, `cmd/fuguegate`) requires that every row with
     gate `release` in the registry is `Done`, and that every live record a live row names is
     valid for the RC (below). It fails with the exact list of what blocks the release.
   - `fuguegate release-evidence` writes `release-evidence.md` (RC commit, release rows,
     live records, CI run) only when the test's checks pass.

The long fuzzing and the twenty-fold test repetition stay in `nightly.yml`; check that its latest
run on the RC's commit is green before tagging.

The RC must be the checked-out commit with no tracked change: the gate refuses anything else,
because tests that ran on other code say nothing about the RC.

## Live records

Evidence that needs a real account or machine is a record the owner creates by hand
(`verification V-06`). The stage A binary has no verification command (`activation A-06`: `fugue`
has only `serve` and `mcp-bridge`), so records come from the repository tool `fuguegate`:

```
go run ./cmd/fuguegate verify-live -id VR-E04 -notes "all three vendors relayed"
```

The command prints the checklist of that ID for the machine it runs on and reads `y` or `n` for
each step. It fills the rest: `revision` (HEAD, which must have no tracked change), `fugue_version`
(`git describe`), `schema_version` (highest migration at HEAD), `protocol_version` (`-protocol-version`,
default `v1`), `os`, `arch`, the UTC times, `result` (`pass` only when every step is `y`) and `covers`.
It writes `quality/records/<ID>/<date>-<os>.json` and refuses to overwrite one. It never reads
vendor output, prompts or account details, and it refuses notes that look like a secret or an email
address. A failed check is recorded with `result: fail` and needs `-notes`.

| ID | Check | Where | Extra input |
| --- | --- | --- | --- |
| `VR-E04` | `approval-relay`: real Claude Code, Codex and agy logins relay approvals verbatim, allow and deny carry the message back, Fugue answers nothing itself | Linux or macOS | none |
| `DA-E01` | `ubuntu-performance-thresholds`: the thresholds of `desktop-app §9` on the owner's Ubuntu machine | Linux | `-measure machine=<label>` |
| `DA-E02` | `ime-telex`: Telex over IBus on Wayland and X11 (run on Ubuntu), and the macOS Telex input source (run on macOS) | both, one record each | none |
| `OR-E01` | `swarm-drill-8-agents`: 8 agents, RAM, CPU and UI response | Linux | `-measure ram_peak_mb=<n> -measure cpu_peak_percent=<n> -measure ui_response_ms=<n>` |

Then name the record in the `Evidence` column of its registry row (several records are separated
by commas) and set the row to `Done`, in the same PR as the record.

A record is valid for an RC when its `result` is `pass`, its `revision` is an ancestor of the RC, and
`git diff <revision>..<RC>` is empty for every glob in `covers`. A change to covered code means the
check runs again.

## Cut an RC

1. Merge everything that belongs in the release. Wait for CI on `main` to be green.
2. Run the live checks on your machines (above), commit the records and the registry update in
   one PR, and merge it.
3. Choose the RC commit: the tip of `main`. Tag it `v<version>-rc.<n>` so the tag is the RC.
4. Run the gate on both operating systems, either way:
   - By hand, on the machine: `git fetch --tags && git checkout v<version>-rc.<n> && go tool task release-check`.
     On the owner's macOS this needs the same toolchain as the CI runner: Go from `go.mod`, git, and
     network access for `govulncheck` and `buf`. Add `CI_RUN=<url>` to record a CI run.
   - In CI: run the `release` workflow from the RC tag (Actions, `release`, Run workflow, select
     the tag). It runs both operating systems and uploads `release-evidence.md` per OS.
5. If `TestReleaseEvidence` fails, its output lists each blocker. Fix them on `main` and cut the
   next RC; the evidence file of an earlier RC is never reused.
6. Attach `release-evidence.md` (both operating systems) to the release tag. The `stable` gate
   (`activation §6`) is a later owner decision and is not part of this gate.

## Running the check on its own

```
FUGUE_RC_COMMIT=$(git rev-parse HEAD) go test -tags release -count=1 -run '^TestReleaseEvidence$' ./cmd/fuguegate
```

An unset `FUGUE_RC_COMMIT` fails the test; it is never skipped.
