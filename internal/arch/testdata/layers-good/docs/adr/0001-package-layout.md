# ADR

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/platform/clock` | 0 | x |  | F0 |
| `internal/kernel` | 1 | x | `internal/platform/clock` | F0 |
| `internal/app` | 4 | x | `internal/kernel`, `internal/platform/clock`, `internal/vendor` | F0 |
| `internal/arch` | test | x |  | F0 |
