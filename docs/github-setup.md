# GitHub setup

The CI workflows (`.github/workflows/pr.yml`, `nightly.yml`) and `.github/CODEOWNERS` live in
the repository, but the settings below belong to the repository owner and are changed in
GitHub, not by an agent. Do them once, in this order. The names `fmt`, `lint`, ... are the job
names of `pr.yml`; branch protection is only as strong as this list.

## 1. Create the approval label

`gate-guard` fails a PR that touches a gate-defining file (quality-gates §10) unless it has
this label **and the owner applied it**.

```bash
gh label create gate-change-approved --repo syurodev/fugue \
  --color B60205 --description "The owner approved a change to a gate-defining file"
```

Apply it to a PR only after reading the diff of the gate files. If a PR gets more commits after
the label, read them again; the label is not removed automatically.

## 2. Merge settings

Squash merge only, the PR title as the commit subject, an empty body (so GitHub adds no
`Co-authored-by` lines from the PR's commits), and branches deleted after merge.

```bash
gh api -X PATCH repos/syurodev/fugue \
  -F allow_squash_merge=true -F allow_merge_commit=false -F allow_rebase_merge=false \
  -F squash_merge_commit_title=PR_TITLE -F squash_merge_commit_message=BLANK \
  -F delete_branch_on_merge=true
```

## 3. Protect `main`

A ruleset that requires a pull request (squash only), every check below, an up-to-date branch,
and forbids force pushes and deletion, with no bypass:

```bash
gh api -X POST repos/syurodev/fugue/rulesets --input - <<'JSON'
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": false,
        "allowed_merge_methods": ["squash"] } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          { "context": "fmt" }, { "context": "lint" }, { "context": "vet" },
          { "context": "arch" },
          { "context": "test (ubuntu-latest)" }, { "context": "test (macos-latest)" },
          { "context": "coverage" }, { "context": "deadcode" }, { "context": "mod" },
          { "context": "proto" }, { "context": "fuzz-short" },
          { "context": "mutation-changed" }, { "context": "gate-guard" },
          { "context": "commit-lint" } ] } }
  ]
}
JSON
```

Why no required reviews: the repository has one owner, who cannot approve their own pull
request, so a required review would block every merge. `gate-guard` and the label take its
place for gate files. `CODEOWNERS` still lists the owner for everything and is what a second
maintainer's required review would build on.

The status checks appear in the "required" list only after each has run once, so open a first
PR (any change) before creating the ruleset, or create it and expect the first PR to wait for
checks that have not been reported yet.

## 4. Actions settings (recommended)

In Settings, Actions, General: set workflow permissions to read-only, and under "Actions
permissions" allow only actions created by GitHub. Every `uses:` in the workflows is pinned to a
full commit SHA; `TestActionsArePinnedToCommits` keeps it so.

## 5. Caches

`actions/setup-go` caches the Go module and build caches, keyed on `go.sum`. A cache written
by a pull request is visible only to that pull request, and a key is written only once, so
without help every new PR starts cold. The `cache` workflow builds everything the CI builds on
`main` (after each merge and daily, since GitHub drops a cache unused for seven days), and every
PR restores it. The `coverage` job reuses the profile the ubuntu `test` job produced instead of
running the suite again. A PR that changes `go.sum` starts cold once.

## 6. Check that it blocks

"A trial PR violating each gate is blocked" is the evidence for F0.6. After steps 1 to 3, an
agent can open these draft PRs from throwaway branches (ask first; each is closed unmerged):

| Trial change | Expected failing check |
| --- | --- |
| A badly formatted Go file | `fmt` |
| A new linter finding, such as an unchecked error | `lint` |
| A `go` statement outside `supervise` | `vet` |
| A package missing from the layer table | `arch` |
| A failing test | `test (ubuntu-latest)`, `test (macos-latest)` |
| An untested package | `coverage` |
| An unreachable function | `deadcode` |
| An untidy `go.mod` | `mod` |
| A stale generated file | `proto` |
| An edit to `Taskfile.yml` without the label | `gate-guard` |
| The same edit with the label applied by someone else | `gate-guard` |
| A commit with a `Co-Authored-By` trailer, or the title `WIP` | `commit-lint` |

## 7. Local hooks

Each contributor runs `task hooks` once. It points git at `.githooks/`, whose `commit-msg`
hook runs the same rules as the `commit-lint` job on every commit message.
