# Fugue

Fugue by SynX is a local-first, all-in-one Go core. One host process (`fugue serve`) owns the
data directory, the vault, the execution store and every service. The desktop shell and other
clients talk to it only through the versioned Host API.

The current goal is the native agent harness.

- Work board and status: [docs/roadmap.md](docs/roadmap.md)
- Quality rules and how they are enforced: [docs/quality-gates.md](docs/quality-gates.md)
- Rules for agents working in this repository: [AGENTS.md](AGENTS.md)

## License

Apache License 2.0. See [LICENSE](LICENSE).
