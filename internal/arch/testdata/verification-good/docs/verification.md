# Verification Registry

| Prefix | Source | Count |
| --- | --- | --- |
| `VR-E` | `vendor-runtime §15` | 3 |
| `A-S` | `activation §6` | 1 |

## Not in stage A

| ID | Reason |
| --- | --- |
| `VR-E02` | Gate B |

## Registry

| ID | Source | Gate | Tier | Evidence | Status |
| --- | --- | --- | --- | --- | --- |
| `VR-E01` | `vendor-runtime §15` | `release` | `pr` | `TestCrossPlatform` | `Done` |
| `VR-E03` | `vendor-runtime §15` | `release` | `nightly` | `TestCrossPlatform`, `quality/probes/drop-cas.patch` | `Done` |
| `A-S01` | `activation §6` | `stable` | `live` | `quality/records/A-S01/2026-11-13-linux.json` | `Done` |
