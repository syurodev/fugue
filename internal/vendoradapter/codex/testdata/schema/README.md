# Pinned Codex App-Server JSON Schema (VR-E02)

`codex_app_server_protocol.schemas.json` was produced by running:

```sh
codex app-server generate-json-schema --out internal/vendoradapter/codex/testdata/schema
```

with `codex-cli 0.167.0` (`codex --version` -> `codex-cli 0.167.0`), keeping the bundled protocol schema file `codex_app_server_protocol.schemas.json`.

To regenerate and diff against a newer `codex` binary during a version bump (developer-only, never run in CI):

```sh
go tool task -d internal/vendoradapter/codex schema-diff
```
