# ADR 0002

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/vendor` | 3 | x | `internal/platform/clock` | F0 |
| `pkg/pb/x` | 5 | x | `pkg/pb/x` | F0 |
| `pkg/client` | 6 | x | `pkg/pb/x` | F0 |
