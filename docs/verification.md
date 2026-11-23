# Verification Registry

Seeded registry of stage A verification IDs (spec item F3.6a, `verification §1–2, §5–6`).
Until `verification` v0.15 settles the stage-A gates and counts for every prefix (`Q-81`), only the
five prefixes below are seeded. Each row's status reflects only whether its named evidence already
exists on `main` (`V-05`).

## Seeded prefixes

| Prefix | Source | Count |
| --- | --- | --- |
| `VR-E` | `vendor-runtime §15` | 14 |
| `TR-E` | `transcript §9` | 8 |
| `DA-E` | `desktop-app §10` | 8 |
| `A-E` | `activation §5` | 10 |
| `A-S` | `activation §6` | 3 |

## Not in stage A

| ID | Reason |
| --- | --- |
| `A-E09` | Gate B (`activation §5`: OpenAI spike `model-adapter §11.7` and ChatGPT login with a real account, moved to B in v0.14) |
| `VR-E12` | Gate B (`vendor-runtime §15`: GLM through Claude Code via Z.ai) |

## Registry

| ID | Source | Gate | Tier | Evidence | Status |
| --- | --- | --- | --- | --- | --- |
| `VR-E01` | `vendor-runtime §15` | `release` | `pr` | `TestVRE01ClaudeFixturesReplayToGoldenTranscript` | `Done` |
| `VR-E02` | `vendor-runtime §15` | `release` | `pr` | `TestVRE02CodexFixturesReplayToGoldenTranscript`, `TestCodexDecodersMatchThePinnedSchema` | `Done` |
| `VR-E03` | `vendor-runtime §15` | `release` | `pr` | `TestVRE03AgyFixturesReplayToGoldenTranscript`, `TestAgyHandshakeMatchesThePinnedCapabilities` | `Done` |
| `VR-E04` | `vendor-runtime §15` | `release` | `live` | Real-account approval relay for Claude, Codex, and agy (F9.6–F9.8) | `Not started` |
| `VR-E05` | `vendor-runtime §15` | `release` | `pr` | `TestStopOrderImmediateInterrupt`, `TestStopOrderEscalatesToSigint`, `TestStopOrderEscalatesToSigterm`, `TestStopOrderEscalatesToSigkill`, `TestStopKillsGroupMembersAfterLeaderExits`, `TestStopCleansAllDescendantsIncludingSetsidGrandchild`, `TestLinuxCgroupStopProofAndFallback`, `TestCodexStopKillsASetsidChildTree`, `TestAgyStopKillsTheProcessTree` | `Done` |
| `VR-E06` | `vendor-runtime §15` | `release` | `pr` | `TestVRE06HostCrashDuringTurnMarksDetachedAndUnknown`, `TestRecoveryDetachesSessionsAndMarksOpenTurnsUnknown`, `TestReapRefusesMismatchedStartTime` | `Done` |
| `VR-E07` | `vendor-runtime §15` | `release` | `pr` | `TestAgyShellCommandGoesThroughRequestPermissionAndRunsWhenAllowed`, `TestAgyPermissionRelaysVerbatimWithItsOptionsAndIsAnsweredOnce`, `TestFugueNeverAnswersAnAgyPermissionRequestInAnyMode`, `TestAgyRetriedPermissionReplaysTheSameResponse`, `TestAgyPermissionResponseMatchesTheACPSpecShape` | `Done` |
| `VR-E08` | `vendor-runtime §15` | `release` | `pr` | `TestUnknownEventsBecomeUnsupportedNoticesWithRawRef`, `TestVersionOutsideCheckedRangeWarnsButRuns`, `TestCodexUnknownNotificationsBecomeUnsupportedNoticesWithRawRef`, `TestCodexVersionOutsideTheTestedRangeWarnsButRuns`, `TestAgyUnknownUpdatesBecomeUnsupportedNoticesWithRawRef`, `TestAgyVersionOutsideTheTestedRangeWarnsButRuns` | `Done` |
| `VR-E09` | `vendor-runtime §15` | `release` | `pr` | `TestEnvFilterAllowlistAndForcedDumbTerm`, `TestNoVendorCredentialAccess`, `TestProbeEnvIsAllowlistedWithoutHostSecrets`, `TestSessionEnvIsAllowlistedWithoutHostSecrets`, `TestCodexProbeEnvIsAllowlistedWithoutHostSecrets`, `TestCodexSessionEnvIsAllowlistedWithoutHostSecrets`, `TestAgyProbeEnvIsAllowlistedWithoutHostSecrets`, `TestAgySessionEnvIsAllowlistedWithoutHostSecrets` | `Done` |
| `VR-E10` | `vendor-runtime §15` | `release` | `pr` | `TestSlashCommandListHidesTerminalCommandsInVendorOrder`, `TestCodexSlashTableRunsTheMeasuredRPCs`, `TestAgySlashListHidesTerminalCommands`, `TestListCommandsHidesTerminalCommandsAndListsTheFugueGroup`, `TestFugueCommandsNeverReachTheVendor`, `TestFugueNamespaceNeverReachesCodex`, `TestFugueNamespaceNeverReachesAgy` | `Done` |
| `VR-E11` | `vendor-runtime §15` | `release` | `pr` | `TestVRE11VaultLockInterruptsAndWaitsWithoutPlaintext`, `TestProtectedEventsAndVaultLock` | `Done` |
| `VR-E13` | `vendor-runtime §15` | `release` | `pr` | `TestFugueToolFromVendorSessionGoesThroughDecide`, `TestFugueToolFromCodexSessionGoesThroughDecide`, `TestFugueToolFromAgySessionGoesThroughDecide`, `TestProductionWiringHonoursTheEnvTagHardRule` | `Done` |
| `VR-E14` | `vendor-runtime §15` | `release` | `pr` | Per-turn git guard diff checks `GG-E01`–`GG-E05` | `Not started` |
| `TR-E01` | `transcript §9` | `release` | `pr` | `TestVRE01ClaudeFixturesReplayToGoldenTranscript`, `TestVRE02CodexFixturesReplayToGoldenTranscript`, `TestVRE03AgyFixturesReplayToGoldenTranscript` | `Done` |
| `TR-E02` | `transcript §9` | `release` | `pr` | `TestUnsupportedEventBecomesNoticeWithRawRef` | `Done` |
| `TR-E03` | `transcript §9` | `release` | `pr` | `TestTranscriptReconnectAfterLostDeltaGetsOpenItemsAndFinalDurableState` | `Done` |
| `TR-E04` | `transcript §9` | `release` | `pr` | `TestTranscriptAndRawPayloadNeverAppearInVendorColumns`, `TestMetadataTypesHoldNoContentStrings` | `Done` |
| `TR-E05` | `transcript §9` | `release` | `pr` | `TestTranscriptPageIsStableAcrossPagesWithoutDuplicatesOrGaps` | `Done` |
| `TR-E06` | `transcript §9` | `release` | `pr` | `TestOversizedItemsShowTruncatedViewsAndKeepFullContent` | `Done` |
| `TR-E07` | `transcript §9` | `release` | `pr` | `TestStorageFitKeepsHeadAndTailWithinLimit`, `TestOversizedContentAndRawAreStoredHeadAndTailWithFlags` | `Done` |
| `TR-E08` | `transcript §9` | `release` | `pr` | `TestViewsRedactBeforeTruncating`, `TestSecretsAreMaskedInPageItemSnapshotAndDeltaAndStoredPayloadIsOriginal` | `Done` |
| `DA-E01` | `desktop-app §10` | `release` | `live` | Ubuntu performance thresholds live record (F10.4) | `Not started` |
| `DA-E02` | `desktop-app §10` | `release` | `live` | Telex IBus (Ubuntu) and macOS IME live record | `Not started` |
| `DA-E03` | `desktop-app §10` | `release` | `pr` | `TestParseKeepsSpacesAroundCodeAndBold`, `TestParseExpandsTabsAtAFourColumnTabStop`, `TestModelDumpsMatchGoldenFiles`, `TestLayoutUsesTheBundledFontsAndWeights`, `TestNoTabReachesTheShaper` | `Done` |
| `DA-E04` | `desktop-app §10` | `release` | `pr` | `TestAppReconnectsAndCatchesUpFromSequence` | `Done` |
| `DA-E05` | `desktop-app §10` | `release` | `pr` | `TestFailureUXMissingVendorCLIProbesAsTypedError`, `TestFailureUXUnsupportedVersionAndLoggedOutProbeOverRealHost`, `TestFailureUXDetachedSessionResumesAndClosesOverRealHost`, `TestFailureUXVaultLockSuspendsAndResumesSessionOverRealHost` | `Done` |
| `DA-E06` | `desktop-app §10` | `release` | `pr` | `TestCoreNeverImportsUI`, `TestAppWritesOnlyThroughHostAPI` | `Done` |
| `DA-E07` | `desktop-app §10` | `release` | `pr` | `TestProposalCardSubmitsExactEdits`, `TestTeamTreeReusesUnchangedAgentLayouts` | `Done` |
| `DA-E08` | `desktop-app §10` | `release` | `pr` | `TestHostManagerStartsAbsentHost`, `TestBackgroundKeepsRealHostAliveUntilDisabled`, `TestServeExitsAfterLastLeaseIdleGrace`, `TestConcurrentAppStartsShareOneHost` | `Done` |
| `A-E01` | `activation §5` | `release` | `pr` | Gates G-01 to G-06 closed and the G-03 evidence (`storage-security §10`) passing; G-04 on the ChatGPT adapter and tool set | `Not started` |
| `A-E02` | `activation §5` | `release` | `pr` | Headless tests for every group in the validation criteria (`decisions-and-validation`) with the deterministic clock and fault injection | `Not started` |
| `A-E03` | `activation §5` | `release` | `nightly` | Nightly `probes` job (`task probes`, `fuguegate probes`) applies each patch and requires its declared tests to fail: `quality/probes/01-vault-fail-open.patch`, `quality/probes/02-session-principal-escalation.patch`, `quality/probes/03-vendor-auto-approve-bypass.patch`, `quality/probes/04-decide-hard-rule-bypass.patch`, `quality/probes/05-team-write-scope-bypass.patch`, `quality/probes/06-workspace-path-traversal.patch`. The design lists five probes (remove CAS, remove the current-authority check, dispatch on replay, retry unknown, cancellation as stop proof); none of the six patches is one of them | `In progress` |
| `A-E04` | `activation §5` | `release` | `pr` | Invariant checker SM-01 to SM-12 after every batch in the test build and the dogfood build | `Not started` |
| `A-E05` | `activation §5` | `release` | `pr` | Partial, over a real host with a fake vendor CLI (shared with DA-E05): `TestFailureUXMissingVendorCLIProbesAsTypedError`, `TestFailureUXUnsupportedVersionAndLoggedOutProbeOverRealHost`, `TestFailureUXDetachedSessionResumesAndClosesOverRealHost`, `TestFailureUXVaultLockSuspendsAndResumesSessionOverRealHost`. Rows of `activation §3` still without an end-to-end test: host connection lost, `Waiting(recovery_admission)`, `Interrupted(effect_unknown)`, `payload_unavailable`, run context selection, hung worker without stop proof, budget/quota/worker-capacity waits, data directory held by another process, unsupported schema or protocol, paused vendor account, `unknown` task or rejected result gate; the headless host failing closed is not covered either | `In progress` |
| `A-E06` | `activation §5` | `release` | `pr` | Crash points in `cmd/fugue/crash_recovery_test.go`: `TestCrashPointVendorTurnActiveRecovery` (streaming vendor turn), `TestCrashPointInteractionCardPendingRecovery` (pending approval), `TestCrashPointTeamTaskRunningRecovery` (running task), `TestCrashPointTeamMergeAfterFFRecovery` (merge queue). Gap: no crash point for an unknown commit. The tests run on Linux in the PR `test` job and on macOS in `main.yml` (every merge into main) and the nightly `repeat-tests`, not in PR runs unless platform-specific files change | `In progress` |
| `A-E07` | `activation §5` | `release` | `pr` | Sandbox smoke with the three presets on Linux and macOS | `Not started` |
| `A-E08` | `activation §5` | `release` | `nightly` | Upgrade drill (stop host mid-run, install new build, resume through `Waiting(recovery_admission)`) and downgrade drill (old build opening the new data directory does not append) | `Not started` |
| `A-E10` | `activation §5` | `release` | `release` | Release-gate evidence of vendor-runtime, transcript, orchestration and desktop-app all passing | `Not started` |
| `A-S01` | `activation §6` | `stable` | `live` | Recovery drill live records (>= 20 kills, >= 5 on macOS) | `Not started` |
| `A-S02` | `activation §6` | `stable` | `live` | Safety live record (0 SM violations, 0 double-dispatch, 0 unexplained `Interrupted`) | `Not started` |
| `A-S03` | `activation §6` | `stable` | `live` | Prompt-to-vendor-stdin latency live records (p50 <= 100 ms, p95 <= 300 ms) | `Not started` |


## F11.7b team UI evidence

- DA-E07 performance: `TestTeamTreeLayoutBudget` (manual run with the `perf` build tag). The
  test measures the p95 layout time for a 12-agent tree during continuous updates.
- DA-E11 Chat/Team switch: `TestTeamSwitchPreservesChatAndComposer`.
- DA-E13 Channel: `TestTeamChannelThreadsRoutingAndInbox`,
  `TestTeamChannelRestoresUnreadPositionFromLayout`, `TestWorkerPassesThreadAndExplicitTargetToHost`,
  and `TestUserPostRoutesByMentionAndThread`.
