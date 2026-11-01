# ADR

| Package | Layer | Purpose and owner | May import | Phase |
| --- | --- | --- | --- | --- |
| `internal/storage` | 1 | x |  | F0 |
| `internal/execstore` | 3 | x |  | F0 |
| `internal/coordinator` | 3 | x |  | F0 |

| Table | Owner | Phase |
| --- | --- | --- |
| `schema_migrations` | `internal/storage` | F1 |
| `exec_events` | `internal/storage` | F1 |
| `exec_events` | `internal/execstore` | F1 |
| `exec_runs` | `internal/ghost` | F1 |

