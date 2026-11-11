# Dependencies

Every module that `go.mod` requires directly, and every module that provides a `tool`
directive, has a row here with a reason, a license and an approver. The `ui/` module
(`github.com/syurodev/fugue/ui`, ADR 0002) has its own `go.mod`; its modules have their own
section below, governed the same way. Licenses allowed without
asking: MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC; anything else needs the user's
approval (`quality-gates` §7). Modules that appear only as `// indirect` are covered by
`go-licenses check` and `govulncheck`, not by a row.

Changing this file needs the `gate-change-approved` label. `TestDependencyAllowlist` (F0.5)
checks it against `go.mod`.

## Modules

| Module | License | Reason | Approver | Enters |
| --- | --- | --- | --- | --- |
| `github.com/google/go-cmp` | BSD-3-Clause | Test comparisons (`quality-gates` §6) | user, ADR 0001 | F0.3 |
| `google.golang.org/protobuf` | BSD-3-Clause | Runtime of the generated message code; `protoc-gen-go` plugin | user, ADR 0001 | F0.7 |
| `connectrpc.com/connect` | Apache-2.0 | Connect runtime for the generated handlers; `protoc-gen-connect-go` plugin | user, ADR 0001 | F0.7 |
| `go.uber.org/goleak` | MIT | `TestMain` goroutine-leak check in every package | user, ADR 0001 | F0.3 |
| `golang.org/x/crypto` | BSD-3-Clause | Argon2id and XChaCha20-Poly1305 for `internal/crypto` | user, ADR 0001 | F1.1 |
| `modernc.org/sqlite` | BSD-3-Clause | Pure-Go SQLite driver for `internal/storage` | user, ADR 0001 | F1.3 |
| `golang.org/x/sys` | BSD-3-Clause | `flock`, `statfs`, peer credentials and landlock for `internal/storage`, `internal/sandbox` and `internal/hostapi` | user, ADR 0001 | F1.3 |
| `pgregory.net/rapid` | MPL-2.0 (approved: Q-06, tests only) | F2.2 property tests only; excluded from production imports; [license and provenance](third-party-licenses/rapid/README.md) | user, Q-06, 2026-11-02 | F2.2 |

## Dev tools (`tool` directives, not linked into the binary)

| Module | License | Reason | Approver | Enters |
| --- | --- | --- | --- | --- |
| `mvdan.cc/gofumpt` | BSD-3-Clause | Stricter `gofmt`; the same rules run inside golangci-lint | user, ADR 0001 | F0.3 |
| `golang.org/x/tools` | BSD-3-Clause | `deadcode`; later `go/analysis` for `fuguevet` | user, ADR 0001 | F0.3 |
| `golang.org/x/vuln` | BSD-3-Clause | `govulncheck` | user, ADR 0001 | F0.3 |
| `github.com/google/go-licenses` | Apache-2.0 | License policy check | user, ADR 0001 | F0.3 |
| `github.com/go-gremlins/gremlins` | Apache-2.0 | Mutation testing | user, ADR 0001 | F0.3 |
| `github.com/rhysd/actionlint` | MIT | Lint of the GitHub Actions workflows (`task actions`) | user, ADR 0001 (approved with PR review) | F0.6 |
| `github.com/go-task/task/v3` | MIT | `Taskfile.yml` runner, so every machine runs the same version | user, ADR 0001 | F0.3 |
| `github.com/bufbuild/buf` | Apache-2.0 | Proto lint, format, breaking-change check and code generation | user, ADR 0001 | F0.7 |

TypeScript generation uses the remote plugin `buf.build/bufbuild/es` (Apache-2.0), pinned by version in `proto/buf.gen.yaml`; it needs network access when `task generate` or `task proto` runs.

## The ui module (`github.com/syurodev/fugue/ui`, enters F10.2a)

Rows for the desktop app's own `go.mod` (ADR 0002 §6); none of them reach the core module.

| Module | License | Reason | Approver | Enters |
| --- | --- | --- | --- | --- |
| `gioui.org` | Unlicense OR MIT | Gio immediate-mode GUI toolkit of the desktop app (DA-01) | user, ADR 0002 | F10.2a |
| `golang.org/x/image` | BSD-3-Clause | `font/sfnt` glyph-index checks of the bundled fonts in tests | user, ADR 0002 | F10.2a |
| `github.com/yuin/goldmark` | MIT | CommonMark parsing of vendor markdown into the immutable model of `ui/internal/markdown` (DA-03) | user, roadmap F10.3a | F10.3a |
| `github.com/alecthomas/chroma/v2` | MIT | Syntax tokenization of fenced code blocks into stable roles in `ui/internal/markdown`; lexer only, never the HTML formatter (DA-03) | user, roadmap F10.3a | F10.3a |

Bundled static assets of `ui/` (DA-04), not Go modules:

| Asset | License | Source | Approver |
| --- | --- | --- | --- |
| `ui/internal/fonts/data/Inter-Variable.ttf` | OFL-1.1 | [rsms/inter v4.1](https://github.com/rsms/inter/releases/tag/v4.1) | user, ADR 0002 |
| `ui/internal/fonts/data/JetBrainsMono-Variable.ttf` | OFL-1.1 | [JetBrains/JetBrainsMono v2.304](https://github.com/JetBrains/JetBrainsMono/releases/tag/v2.304) | user, ADR 0002 |

The OFL-1.1 license files ship beside the fonts (`Inter-OFL.txt`, `JetBrainsMono-OFL.txt`); OFL-1.1 is
an approved open font license per ADR 0002 and is not on the code-license list above because it
licenses embedded font data, not linked code.

## Pinned outside `go.mod`

| Tool | License | Pin | Why not a `tool` directive |
| --- | --- | --- | --- |
| `github.com/golangci/golangci-lint/v2` | GPL-3.0 (run as a program, never linked) | `GOLANGCI_LINT_VERSION` in `Taskfile.yml` | Its module graph is large and moves with the Go release; it runs as a program through `go run <module>@<version>`. Running a GPL tool does not make this repository GPL. ADR 0001 names this pin; the license is outside the allowed list, so it needs the user's explicit approval in review of this file. |
