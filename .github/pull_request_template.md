## Roadmap item

<!-- ID and link to its row in docs/roadmap.md, for example [F2.1](../blob/main/docs/roadmap.md) -->

## Summary

<!-- What changes and why. -->

## Evidence

<!-- Named tests added or changed. They must match the Evidence column of the roadmap row. -->

## Gates

- [ ] Touches a gate-defining file (`.golangci.yml`, `tools/fuguevet/`, `internal/arch/`, `quality/`, `docs/quality-gates.md`, `docs/dependencies.md`, `.github/workflows/`, `Taskfile.yml`). If so, the `gate-change-approved` label is required.
- [ ] Changes a threshold or baseline. If so, say which and in which direction:

## Design questions

<!-- Rows added to "Needs decision" in docs/roadmap.md §7, or "none". -->

## Security notes

<!-- Secrets, vault, sandbox, authorization. "None" if the change does not touch them. -->

## Checklist

- [ ] `task check` is green
- [ ] The roadmap row (status, PR link, evidence, log line) is updated in this PR
- [ ] No new `nolint` or `t.Skip`
- [ ] No new dependency without a row in `docs/dependencies.md`
- [ ] No `Co-Authored-By` in commits; the PR title is a Conventional Commit with the item ID
