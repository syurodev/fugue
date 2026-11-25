# Fugue

Fugue by SynX is a local-first, all-in-one Go core. One host process (`fugue serve`) owns the
data directory, the vault, the execution store and every service. The desktop shell and other
clients talk to it only through the versioned Host API.

The current goal is stage A: drive the first-party agent CLIs (Claude Code, Codex, Antigravity)
under the user's own logins, show them in a Gio desktop app, and let agents on different
vendors work as a team. The native agent harness follows in stage B.

- Work board and status: [Fugue team in Linear](https://linear.app/fugue) (`FUG-*`)
- Release verification evidence: [docs/verification.md](docs/verification.md)
- Quality rules and how they are enforced: [docs/quality-gates.md](docs/quality-gates.md)
- Rules for agents working in this repository: [AGENTS.md](AGENTS.md)

## Run from source

```sh
task run                                             # build bin/fugue and bin/fugue-app, start the app
SYNX_FUGUE_DATA_DIR=/tmp/fugue-dev task run          # same, with a scratch data directory
task build                                           # build only
```

The app starts its host (`fugue serve`) from the binary next to it, so both are built into
`bin/`. Without `SYNX_FUGUE_DATA_DIR` it uses your real data directory and your real vendor
logins.

## License

Apache License 2.0. See [LICENSE](LICENSE).
