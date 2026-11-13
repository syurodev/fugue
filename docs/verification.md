# Verification Registry

Seeded registry of stage A verification IDs (`docs/roadmap.md` F3.6a, `verification §1–2, §5–6`).
Until `verification` v0.15 settles the stage-A gates and counts for every prefix (`Q-81`), only the
four prefixes below are seeded. Each row's status reflects only whether its named evidence already
exists on `main` (`V-05`).

## Seeded prefixes

| Prefix | Source | Count |
| --- | --- | --- |
| `VR-E` | `vendor-runtime §15` | 14 |
| `TR-E` | `transcript §9` | 8 |
| `DA-E` | `desktop-app §10` | 8 |
| `A-S` | `activation §6` | 3 |

## Not in stage A

| ID | Reason |
| --- | --- |
| `VR-E12` | Gate B (`vendor-runtime §15`: GLM through Claude Code via Z.ai) |

## Registry

| ID | Source | Gate | Tier | Evidence | Status |
| --- | --- | --- | --- | --- | --- |
| `VR-E01` | `vendor-runtime §15` | `release` | `pr` | `TestVRE01ClaudeFixturesReplayToGoldenTranscript` | `Done` |
| `VR-E02` | `vendor-runtime §15` | `release` | `pr` | Codex conformance and pinned JSON schema check (F9.7) | `Not started` |
| `VR-E03` | `vendor-runtime §15` | `release` | `pr` | Antigravity conformance and ACP handshake (F9.8) | `Not started` |
| `VR-E04` | `vendor-runtime §15` | `release` | `live` | Real-account approval relay for Claude, Codex, and agy (F9.6–F9.8) | `Not started` |
| `VR-E05` | `vendor-runtime §15` | `release` | `pr` | `TestStopOrderImmediateInterrupt`, `TestStopOrderEscalatesToSigint`, `TestStopOrderEscalatesToSigterm`, `TestStopOrderEscalatesToSigkill`, `TestStopKillsGroupMembersAfterLeaderExits`, `TestStopCleansAllDescendantsIncludingSetsidGrandchild`, `TestLinuxCgroupStopProofAndFallback` | `Done` |
| `VR-E06` | `vendor-runtime §15` | `release` | `pr` | `TestVRE06HostCrashDuringTurnMarksDetachedAndUnknown`, `TestRecoveryDetachesSessionsAndMarksOpenTurnsUnknown`, `TestReapRefusesMismatchedStartTime` | `Done` |
| `VR-E07` | `vendor-runtime §15` | `release` | `pr` | agy ACP `session/request_permission` flow (F9.8) | `Not started` |
| `VR-E08` | `vendor-runtime §15` | `release` | `pr` | `TestUnknownEventsBecomeUnsupportedNoticesWithRawRef`, `TestVersionOutsideCheckedRangeWarnsButRuns` | `Done` |
| `VR-E09` | `vendor-runtime §15` | `release` | `pr` | `TestEnvFilterAllowlistAndForcedDumbTerm`, `TestNoVendorCredentialAccess`, `TestProbeEnvIsAllowlistedWithoutHostSecrets`, `TestSessionEnvIsAllowlistedWithoutHostSecrets` | `Done` |
| `VR-E10` | `vendor-runtime §15` | `release` | `pr` | Claude/agy/Codex slash command lists and `/fugue:` blocking (F9.7–F9.9d) | `Not started` |
| `VR-E11` | `vendor-runtime §15` | `release` | `pr` | `TestVRE11VaultLockInterruptsAndWaitsWithoutPlaintext`, `TestProtectedEventsAndVaultLock` | `Done` |
| `VR-E13` | `vendor-runtime §15` | `release` | `pr` | Fugue MCP tool `Decide` env-tag hard rule from vendor session (F9.6) | `Not started` |
| `VR-E14` | `vendor-runtime §15` | `release` | `pr` | Per-turn git guard diff checks `GG-E01`–`GG-E05` | `Not started` |
| `TR-E01` | `transcript §9` | `release` | `pr` | Golden item sequences for Claude, Codex, and agy (F9.7–F9.8) | `Not started` |
| `TR-E02` | `transcript §9` | `release` | `pr` | `TestUnsupportedEventBecomesNoticeWithRawRef` | `Done` |
| `TR-E03` | `transcript §9` | `release` | `pr` | `TestTranscriptReconnectAfterLostDeltaGetsOpenItemsAndFinalDurableState` | `Done` |
| `TR-E04` | `transcript §9` | `release` | `pr` | `TestTranscriptAndRawPayloadNeverAppearInVendorColumns`, `TestMetadataTypesHoldNoContentStrings` | `Done` |
| `TR-E05` | `transcript §9` | `release` | `pr` | `TestTranscriptPageOver10000ItemsIsStableWithoutDuplicatesOrGaps` | `Done` |
| `TR-E06` | `transcript §9` | `release` | `pr` | `TestOversizedItemsShowTruncatedViewsAndKeepFullContent` | `Done` |
| `TR-E07` | `transcript §9` | `release` | `pr` | `TestStorageFitKeepsHeadAndTailWithinLimit`, `TestOversizedContentAndRawAreStoredHeadAndTailWithFlags` | `Done` |
| `TR-E08` | `transcript §9` | `release` | `pr` | `TestViewsRedactBeforeTruncating`, `TestSecretsAreMaskedInPageItemSnapshotAndDeltaAndStoredPayloadIsOriginal` | `Done` |
| `DA-E01` | `desktop-app §10` | `release` | `live` | Ubuntu performance thresholds live record (F10.4) | `Not started` |
| `DA-E02` | `desktop-app §10` | `release` | `live` | Telex IBus (Ubuntu) and macOS IME live record | `Not started` |
| `DA-E03` | `desktop-app §10` | `release` | `pr` | `TestParseKeepsSpacesAroundCodeAndBold`, `TestParseExpandsTabsAtAFourColumnTabStop`, `TestModelDumpsMatchGoldenFiles`, `TestLayoutUsesTheBundledFontsAndWeights`, `TestNoTabReachesTheShaper` | `Done` |
| `DA-E04` | `desktop-app §10` | `release` | `pr` | `TestAppReconnectsAndCatchesUpFromSequence` | `Done` |
| `DA-E05` | `desktop-app §10` | `release` | `pr` | Failure UX E2E tests with real host and fake vendor (F10.5) | `Not started` |
| `DA-E06` | `desktop-app §10` | `release` | `pr` | `TestCoreNeverImportsUI`, `TestAppWritesOnlyThroughHostAPI` | `Done` |
| `DA-E07` | `desktop-app §10` | `release` | `pr` | Team proposal edit and 12-agent team table thresholds (F11.7) | `Not started` |
| `DA-E08` | `desktop-app §10` | `release` | `pr` | `TestHostManagerStartsAbsentHost`, `TestBackgroundKeepsRealHostAliveUntilDisabled`, `TestServeExitsAfterLastLeaseIdleGrace`, `TestConcurrentAppStartsShareOneHost` | `Done` |
| `A-S01` | `activation §6` | `stable` | `live` | Recovery drill live records (>= 20 kills, >= 5 on macOS) | `Not started` |
| `A-S02` | `activation §6` | `stable` | `live` | Safety live record (0 SM violations, 0 double-dispatch, 0 unexplained `Interrupted`) | `Not started` |
| `A-S03` | `activation §6` | `stable` | `live` | Prompt-to-vendor-stdin latency live records (p50 <= 100 ms, p95 <= 300 ms) | `Not started` |
