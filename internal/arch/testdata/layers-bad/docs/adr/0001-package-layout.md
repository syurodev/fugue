# ADR

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/platform/clock` | 0 | x | `internal/kernel` | F0 |
| `internal/kernel` | 1 | x |  | F0 |
| `internal/app` | 4 | x | `internal/kernel`, `internal/ghost` | F0 |
| `internal/arch` | test | x | `internal/kernel` | F0 |

