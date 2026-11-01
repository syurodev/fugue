<!-- PR title: a Conventional Commit, for example `fix(storage): reject a wrong pragma`. Add the roadmap ID in parentheses only when the change belongs to one. -->

## Description

<!-- What this PR does and why, in a few sentences. A reviewer should understand the purpose from this section alone. -->

## Type of change

- [ ] Feature
- [ ] Bug fix (the bug fix template: add `?template=bugfix.md` to the PR URL, or `gh pr create --template bugfix.md`)
- [ ] Refactor (no behavior change)
- [ ] Tests only
- [ ] Documentation
- [ ] Build, CI or tooling
- [ ] Port from DevKit (source path and commit recorded in `docs/ports.md`)

## Changes made

<!-- Specific changes, one per line: "Added X to Y", not "updated files". -->

-

## How to test

<!-- Commands a reviewer runs, and the named tests that prove the change. -->

1. `task check`
2.

## Risk

<!-- Delete the lines that do not apply. -->

- **Security:** touches secrets, the vault, the sandbox or authorization? How is it covered?
- **Gates:** touches a gate-defining file, a threshold or a baseline? This needs the `gate-change-approved` label.
- **Data:** changes a migration, a table or a stored format?
- **Host API:** changes a `.proto` contract? `buf breaking` result?

## Checklist

- [ ] `task check` passes locally
- [ ] Self-reviewed the diff
- [ ] New behavior has named tests; a bug fix has a regression test
- [ ] No new `nolint` or `t.Skip`
- [ ] No new dependency without a row in `docs/dependencies.md`
- [ ] Docs updated: the roadmap row if this belongs to a roadmap item, an ADR if a decision changed
- [ ] No `Co-Authored-By` or other attribution trailer in commits

## Related

<!-- Issues ("Closes #123"), roadmap item, design pages, ADRs, other PRs. "None" is fine. -->
