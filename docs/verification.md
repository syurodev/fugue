# Verification Registry

Seeded registry of stage A verification IDs (spec item F3.6a, `verification §1–2, §5–6`).
The stage A prefixes of `verification §2` are seeded (`Q-81`, FUG-54); the other prefixes of
`verification §2` join when their stage-A parts are settled. Each row's status reflects only whether its named evidence already
exists on `main` (`V-05`).

## Seeded prefixes

| Prefix | Source | Count |
| --- | --- | --- |
| `VR-E` | `vendor-runtime §15` | 14 |
| `TR-E` | `transcript §9` | 8 |
| `DA-E` | `desktop-app §10` | 8 |
| `A-E` | `activation §5` | 10 |
| `A-S` | `activation §6` | 3 |
| `OR-E` | `orchestration §15` | 22 |
| `S-E` | `storage-security §10` | 13 |
| `I-E` | `interaction §11` | 11 |
| `HA-E` | `host-api §10` | 9 |
| `CL-E` | `cli §9` | 12 |

## Not in stage A

| ID | Reason |
| --- | --- |
| `A-E04` | Gate B (`activation §5`: invariant checker SM-01 to SM-12 on the native kernel, moved to B in v0.17; the kernel has no production caller in stage A) |
| `A-E07` | Gate B (`activation §5`: sandbox smoke with the three presets, moved to B in v0.18; the per-session sandbox jail is stage B, FUG-26) |
| `A-E09` | Gate B (`activation §5`: OpenAI spike `model-adapter §11.7` and ChatGPT login with a real account, moved to B in v0.14) |
| `VR-E12` | Gate B (`vendor-runtime §15`: GLM through Claude Code via Z.ai) |
| `S-E02` | Gate B (`storage-security §10`: read-back after a driver error around COMMIT; the execution store `exec_*` has no production caller in stage A) |
| `S-E06` | Gate B (`storage-security §10`: execution store codec conflicts and receipts) |
| `S-E10` | Gate B (`storage-security §10`: active-time tranches of long runs, execution store only) |
| `S-E11` | Gate B (`storage-security §10`: metadata-only dedup cancel and `CloseRunWithoutPayload`, execution store only) |
| `S-E12` | Gate B (`storage-security §10`: `exec_context_decisions` context chain, execution store only) |
| `CL-E05` | Gate B (`cli §9`: teatest golden tests of the TUI; the client CLI/TUI is stage B) |
| `CL-E06` | Gate B (`cli §9`: TUI key table) |
| `CL-E07` | Gate B (`cli §9`: TUI scrollback and live area) |
| `CL-E08` | Gate B (`cli §9`: English UI strings in `internal/tui`) |
| `CL-E10` | Gate B (`cli §9`: `fugue run` with `--on-approval` and `--detach`) |
| `CL-E11` | Gate B (`cli §9`: password through hidden input or `--password-stdin`) |
| `CL-E12` | Gate B (`cli §9`: TUI display and exit code for every row of `cli §6`) |

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
| `VR-E14` | `vendor-runtime §15` | `release` | `pr` | Per-turn git guard diff checks `GG-E01`–`GG-E05`. Partial (FUG-48): `GG-E01` `TestVRE14GGE01ShellChangeAppearsInTurnDiff`; `GG-E02` `TestVRE14GGE02UserGitUntouchedAndNoGitDirectoryWorks`; `GG-E03` `TestVRE14GGE03SnapshotTimeoutIsUnavailableAndTurnCompletes`, `TestVRE14GGE03SnapshotFailureIsUnavailableAndTurnCompletes` `TestVRE14TurnDiffsOutliveTheSessionTurnQuota`, `TestVRE14SnapshotRecordsAreReleasedAfterTurnsAndSession`, `TestVRE14TurnDuringWarmUpWaitsAndGetsACorrectStartSnapshot` (long sessions, bounded journal, warm-up) and the 100 thousand file timing spike `TestTurnSnapshot100kFilesFitsTheCap` (`internal/perf`, nightly `perf` tag). `GG-E05` (FUG-50, end-of-turn secret scan) `TestVRE14GGE05PlantedSecretInAddedLinesRaisesMaskedNotice` (value absent from the database file, logs and metadata), `TestVRE14GGE05RemovedAndUnchangedSecretsDoNotTrigger`, `TestVRE14GGE05NoHitRaisesNoNotice`, `TestVRE14GGE05AboveTheScanCapScansTheHeadAndSaysIncomplete`, `TestVRE14GGE05ScanFailureKeepsHitsAndTheTurnCompletes`, `TestVRE14GGE05ShortValueShowsRuleOnly`, `TestVRE14GGE05NoticeMetadataAllowlistHoldsCountsOnly`. Pending: `GG-E04` (overlap, FUG-49) | `In progress` |
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
| `A-E01` | `activation §5` | `release` | `pr` | Gates G-01 to G-06 closed with the stage A evidence of `activation §5` v0.17 (native-only parts moved to B). G-01: `DA-E06`, `HA-E06` (`Done`). G-02: `A-E03`, `A-E06`, `VR-E06`, `OR-E09` (`Done`). G-03: `TR-E04`, `TR-E08`, `VR-E11`, `S-E03`, `S-E08`, `S-E13` (`Done`); `S-E01`, `S-E04`, `S-E05`, `S-E07`, `S-E09` (`In progress`, stage A parts). G-04: `VR-E01` to `VR-E03`, `VR-E09`, `VR-E13`, `OR-E10`, `OR-E11` (`Done`), `VR-E04` (`Not started`, live). G-05: `DA-E05`, `VR-E13`, `I-E01` to `I-E09`, `I-E11` (`Done`); `I-E10` (`In progress`); `VR-E04` (`Not started`, live). G-06: `HA-E05`, `A-E08` (`Done`). The row stays `In progress` until every listed item is `Done` | `In progress` |
| `A-E02` | `activation §5` | `release` | `pr` | Headless validation criteria (`decisions-and-validation`) met on stage A code (`activation §5` v0.16 and v0.17): real `fugue serve` with a fake vendor CLI replaying fixtures, deterministic clock and fault injection. Evidence families: `VR-E01` to `VR-E03`, `VR-E05`, `VR-E06`, `VR-E08`, `VR-E11` (`Done`); `TR-E01` to `TR-E03` (`Done`); `DA-E04`, `DA-E05`, `DA-E08` (`Done`); `A-E06` (`Done`); `OR-E02`, `OR-E07` to `OR-E09` and `S-E08` (`Done`); `S-E07` (`In progress`). `activation §5` names `S-E11`, but `storage-security §10` marks it stage B (execution store only), so it is not listed here. The VC scenario split between stage A code and native kernel (`VC-03`, `VC-12`, `VC-21`, `VC-27` go to B) is pending Linear FUG-39, so this row cannot close before it | `In progress` |
| `A-E03` | `activation §5` | `release` | `nightly` | Nightly `probes` job (`task probes`, `fuguegate probes`) applies each patch and requires its declared tests to fail (combined probe set, `activation §5` v0.15): `quality/probes/01-vault-fail-open.patch`, `quality/probes/02-session-principal-escalation.patch`, `quality/probes/03-vendor-auto-approve-bypass.patch`, `quality/probes/04-decide-hard-rule-bypass.patch`, `quality/probes/05-team-write-scope-bypass.patch`, `quality/probes/06-workspace-path-traversal.patch`, `quality/probes/07-vendor-session-sequence-cas.patch`, `quality/probes/08-team-sequence-cas.patch`, `quality/probes/09-appstate-revision-cas.patch`, `quality/probes/10-stale-interaction-incarnation.patch`, `quality/probes/11-delegator-authority-bypass.patch`, `quality/probes/12-restart-requeues-unknown-tasks.patch`, `quality/probes/13-restart-leaves-open-turns.patch`, `quality/probes/14-retry-unknown-task.patch`, `quality/probes/15-close-without-stop-proof-recorded-proved.patch`, `quality/probes/16-unproved-stop-releases-peer-owner.patch`, `quality/probes/17-signal-result-claims-stop-proof.patch` | `Done` |
| `A-E05` | `activation §5` | `release` | `pr` | Partial, over a real host with a fake vendor CLI (shared with DA-E05): `TestFailureUXMissingVendorCLIProbesAsTypedError`, `TestFailureUXUnsupportedVersionAndLoggedOutProbeOverRealHost`, `TestFailureUXDetachedSessionResumesAndClosesOverRealHost`, `TestFailureUXVaultLockSuspendsAndResumesSessionOverRealHost`. Rows of `activation §3` still without an end-to-end test: host connection lost, `Waiting(recovery_admission)`, `Interrupted(effect_unknown)`, `payload_unavailable`, run context selection, hung worker without stop proof, budget/quota/worker-capacity waits, data directory held by another process, unsupported schema or protocol, paused vendor account, `unknown` task or rejected result gate; the headless host failing closed is not covered either | `In progress` |
| `A-E06` | `activation §5` | `release` | `pr` | Crash points in `cmd/fugue/crash_recovery_test.go`: `TestCrashPointVendorTurnActiveRecovery` (streaming vendor turn), `TestCrashPointInteractionCardPendingRecovery` (pending approval), `TestCrashPointTeamTaskCommittedRecovery` (unknown commit: the task-branch commit exists, the report event does not), `TestCrashPointTeamTaskRunningRecovery` (running task), `TestCrashPointTeamMergeAfterFFRecovery` (merge queue). The tests run on Linux in the PR `test` job and on macOS in `main.yml` (every merge into main) and the nightly `repeat-tests`, not in PR runs unless platform-specific files change | `Done` |
| `A-E08` | `activation §5` | `release` | `nightly` | Stage A has no `Waiting(recovery_admission)` (native kernel, stage B); the equivalent is a vendor session `detached` with its open turn `unknown`, not replayed, and the transcript still readable. Upgrade drill: `TestUpgradeDrillSameBuildResumesRecovery` (kill mid-run, restart), and in the nightly `drill` job (`task drill`) the same kill with an old build from the merge-base (or the previous schema generation) resumed by the HEAD build. Downgrade drill: `TestDowngradeDrillRefusesNewerSchemaWithoutAppending` (synthetic newer schema: typed `failed_precondition`, no socket, host incarnation unchanged), `TestOpenStoreRefusesNewerSchemaWithoutWriting`, and in the nightly job the old build refusing the new data directory | `Done` |
| `A-E10` | `activation §5` | `release` | `release` | Release-gate evidence of vendor-runtime, transcript, orchestration and desktop-app all passing | `Not started` |
| `A-S01` | `activation §6` | `stable` | `live` | Recovery drill live records (>= 20 kills, >= 5 on macOS) | `Not started` |
| `A-S02` | `activation §6` | `stable` | `live` | Safety live record (0 SM violations, 0 double-dispatch, 0 unexplained `Interrupted`) | `Not started` |
| `A-S03` | `activation §6` | `stable` | `live` | Prompt-to-vendor-stdin latency live records (p50 <= 100 ms, p95 <= 300 ms) | `Not started` |
| `OR-E01` | `orchestration §15` | `release` | `live` | Linux swarm drill live record (8 agents: RAM, CPU, UI responsiveness; tune the `§7` defaults) | `Not started` |
| `OR-E02` | `orchestration §15` | `release` | `pr` | `TestTaskStartReturnsImmediatelyAndTheDelegatorIsWokenOnce`, `TestOneDigestForAGroupOfTasks`, `TestDigestWaitsThirtySecondsOrTheWholeDependencyGroup` | `Done` |
| `OR-E03` | `orchestration §15` | `release` | `pr` | `TestWrongOutputSchemaKeepsTheResultWithSchemaValidFalse`, `TestValidateOutputAgainstSchema` | `Done` |
| `OR-E04` | `orchestration §15` | `release` | `pr` | `TestWriteScopeBouncesOnceThenFails`, `TestWriteScopeEdgeCasesAndUnconfiguredGate` | `Done` |
| `OR-E05` | `orchestration §15` | `release` | `pr` | `TestMergeQueueSerializesAndRebasesTheSecondTask`, `TestRebaseConflictAbortsCleanlyAndListsFiles` | `Done` |
| `OR-E06` | `orchestration §15` | `release` | `pr` | `TestParallelWorktreesNoRefLockLeak`, `TestPerRepoMutexKeysOnCanonicalCommonDir`, `TestRefLockErrorRetriesThreeTimesWithBackoff`, `TestRefLockExhaustionReturnsTypedUnavailable` | `Done` |
| `OR-E07` | `orchestration §15` | `release` | `pr` | `TestQuotaGateBlocksTasksAboveEightyFivePercent`, `TestPausedAccountReopensAtResetsAtAndDispatchesQueuedTasks` | `Done` |
| `OR-E08` | `orchestration §15` | `release` | `pr` | `TestTaskTurnFailureQuotaAndAuthNotifiesDelegator`, `TestQuotaExhaustionReassignsTaskToAnotherAccountWithDiffAndSummary`, `TestQuotaExhaustionWithoutAnotherAccountFailsTaskAndNotifiesDelegator` | `Done` |
| `OR-E09` | `orchestration §15` | `release` | `pr` | `TestHostRestartLeavesRunningTasksUnknownAndNotifiesDelegator`, `TestHostStartRecoversRunningTeamTasksAsUnknown`, `TestRestartMarksRunningTasksUnknownAndNeverReruns`, `TestCrashPointTeamTaskRunningRecovery` | `Done` |
| `OR-E10` | `orchestration §15` | `release` | `pr` | `TestMemberCannotCallDelegationTools`, `TestMemberTokenCannotOpenALeadTool`, `TestTeamManagerCannotDelegateOutsideItsPart` | `Done` |
| `OR-E11` | `orchestration §15` | `release` | `pr` | `TestChecksRunThroughPolicyAndHeavySemaphore` | `Done` |
| `OR-E12` | `orchestration §15` | `stable` | `live` | Live record of a real team (Claude lead, Codex and agy managers and members) on a real repository through merge | `Not started` |
| `OR-E13` | `orchestration §15` | `release` | `pr` | `TestNoTaskStartsBeforeApproval`, `TestEditedModelIsTheTaskModel`, `TestApprovedPlanAndChangeListAreDeliveredToTheProposingSession`, `TestSoloKeepsTheSessionSolo` | `Done` |
| `OR-E14` | `orchestration §15` | `release` | `pr` | `TestMemberPermissionRequestReachesTheUserInboxWithoutAMailboxMessage`, `TestParentQuestionReachesOnlyTheDirectDelegator` | `Done` |
| `OR-E15` | `orchestration §15` | `release` | `pr` | `TestLeadLoadWithFakeVendorsStaysUnderTheWakeBound` | `Done` |
| `OR-E16` | `orchestration §15` | `release` | `pr` | `TestRetryRunsAFailedTaskAgainTwiceWithoutApproval`, `TestThirdRetryNeedsAmend`, `TestAmendNeedsTheUserForANewRoleOrMoreAgents` | `Done` |
| `OR-E17` | `orchestration §15` | `release` | `pr` | `TestLayeredIntegrationNeverTouchesTheTargetBranch`, `TestFinalReviewMergesTeamBranchOnlyAfterUserApproval` | `Done` |
| `OR-E18` | `orchestration §15` | `release` | `pr` | `TestDependentTaskStartsOnlyAfterItsDependenciesAreDone`, `TestDependentReceivesSummariesAndReadAccessToTheResults` | `Done` |
| `OR-E19` | `orchestration §15` | `release` | `pr` | `TestChannelProjectsTeamEventsIntoNumberedThreads`, `TestChannelRetryAttemptsShareOneThread`, `TestTeamReadScopesByRoleAndPagesBothWays`, `TestAgentsNeverReceiveTheChannelStream` | `Done` |
| `OR-E20` | `orchestration §15` | `release` | `pr` | `TestSiblingSendOnlyToSameDelegatorAndCapped` | `Done` |
| `OR-E21` | `orchestration §15` | `release` | `pr` | `TestCrossReviewBundlesWithOCRAndVerifiesPositions`, `TestCrossReviewFallsBackWithoutOCR`, `TestConformanceFixturesAndUnsupportedSchema` | `Done` |
| `OR-E22` | `orchestration §15` | `release` | `pr` | `TestReparentedTaskRebasesOntoTheTeamBranch`, `TestManagerCancelRecoversByReparentingLiveChildren` | `Done` |
| `S-E01` | `storage-security §10` | `release` | `pr` | `TestMidCommitFailureRollsBackPayloadEventAndProjection`, `TestAppendBatchCommitsContiguousSequencesAtomically`, `TestTeamStoreAppendIsContiguousAndProjectionIsAtomic`. Stage A part only (vendor store: event, payload, projection); the `exec_*` receipt and admission part is stage B. Partial: one injected commit failure, not a fault between each step | `In progress` |
| `S-E03` | `storage-security §10` | `release` | `pr` | `TestOpenRestrictsPermissionsAndSetsDurabilityPragmas`, `TestVerifyPragmasRejectsWrongJournalModeSynchronousAndForeignKeys`, `TestSQLiteDoesNotCreateUnprotectedWALSidecars` | `Done` |
| `S-E04` | `storage-security §10` | `release` | `pr` | `TestTeamStoreAppendIsContiguousAndProjectionIsAtomic`, `TestPurgeRollbackPreservesPayloadAndPermanentPurgeRevokesIt`. Stage A part only (`vendor_*` tables); the `exec_*` part is stage B. Partial: the `vendor_events` and `vendor_payloads` UPDATE and DELETE triggers have no test, and no test shows a purge removes exactly one session | `In progress` |
| `S-E05` | `storage-security §10` | `release` | `pr` | `TestPurgeRollbackPreservesPayloadAndPermanentPurgeRevokesIt`, `TestCacheCopiesKeysAndShredsOnPurge`, `TestAmbiguousPurgeBarrierBlocksEveryWrite`. Stage A part only (`vendor_session_keys`); the `exec_session_keys` part is stage B. Partial: no test scans the database file for the wrapped key bytes after a purge | `In progress` |
| `S-E07` | `storage-security §10` | `release` | `pr` | `TestObserveReturnsTheSnapshotAndEveryLaterTransitionInOrder`, `TestASlowSubscriberIsMarkedLaggingAndResyncs`, `TestNothingSucceedsAfterLockReturnsWhileWorkIsInFlight`, `TestProtectedEventsAndVaultLock`, `TestOpenPayloadAndReadItemFailClosedWhenVaultLocked`. Partial: no test of unlock after host recovery, none of a lock between consume steps on a headless host without a bridge | `In progress` |
| `S-E08` | `storage-security §10` | `release` | `pr` | `TestTwoProcessesCannotHoldTheDataDirectoryLock`, `TestPathAliasesAndSymlinksContendOnTheSameLock`, `TestCrashReleasesTheLockAndAdvancesHostIncarnation`, `TestNetworkFilesystemIsRejected` | `Done` |
| `S-E09` | `storage-security §10` | `release` | `pr` | `TestPayloadAndSessionLimits`, `TestOversizedContentAndRawAreStoredHeadAndTailWithFlags`. Stage A part only (payload 1 MiB, session 1 GiB on the vendor store); the 64 MiB run limit is stage B. Partial: the vendor store tests assert a refusal, not the typed failure | `In progress` |
| `S-E13` | `storage-security §10` | `release` | `pr` | `TestTableOwnership`. Stage A part only (vendor store tables and `exec_host` have an owner); the `exec_` prefix of the execution store is stage B | `Done` |
| `I-E01` | `interaction §11` | `release` | `pr` | `TestIE01DecideTableAllCombinations`. Stage A: Fugue MCP tools and `checks`; native tools are stage B | `Done` |
| `I-E02` | `interaction §11` | `release` | `pr` | `TestIE02HardRuleAtFullAndPlanModeRefusal`, `TestFugueToolFromVendorSessionGoesThroughDecide`, `TestProductionWiringHonoursTheEnvTagHardRule`. Stage A: Fugue MCP tools and `checks` (`VR-E13`); native tools are stage B | `Done` |
| `I-E03` | `interaction §11` | `release` | `pr` | `TestIE03GrantScopeKeyMatchingAndPolicyRevisionInvalidation`, `TestIE03NilTagPolicyNeverKeysTargetGrants`. Stage A: `grantScopeKey` of Fugue MCP tools; the native `grantScope` is stage B | `Done` |
| `I-E04` | `interaction §11` | `release` | `pr` | `TestIE04DefaultRuleGroups`, `TestIE04AntiBypass`, `TestIE04WorkspaceAllowOnlyExact`, `TestIE04UnparsableAsksInAuto`, `TestIE04TagConfirmationViaRegistry`, `TestChecksRunThroughPolicyAndHeavySemaphore`. Stage A: `checks` commands; the `shell` tool is stage B | `Done` |
| `I-E05` | `interaction §11` | `release` | `pr` | `TestAnOldRequestCannotBeAnsweredAfterRestart`, `TestCrashPointInteractionCardPendingRecovery`, `TestVRE06HostCrashDuringTurnMarksDetachedAndUnknown`. Stage A: a host restart drops requests, grants and unclaimed receipts (`§4.3`), vendor sessions follow `VR-E06`; `ToolCheckReset` is stage B | `Done` |
| `I-E06` | `interaction §11` | `release` | `pr` | `TestIE06VaultLockFailsClosedBetweenAnswerAndClaimOrDelivery`, `TestVRE11VaultLockInterruptsAndWaitsWithoutPlaintext`, `TestSessionToolConfirmationFailsClosedWhileVaultLocked` | `Done` |
| `I-E07` | `interaction §11` | `release` | `pr` | `TestIE07RequestSurvivesClientReconnectInSnapshot`, `TestIE07TwoClientsAnsweringAtOnceExactlyOneIsAccepted`, `TestIE07DuplicateCommandIDReturnsTheSameResult` | `Done` |
| `I-E08` | `interaction §11` | `release` | `pr` | `TestIE08AuditAndErrorsNeverContainRequestContent`, `TestIE08ResolvedEventAndErrorsContainNoContent` | `Done` |
| `I-E09` | `interaction §11` | `release` | `pr` | `TestInvalidEnumsAndPolicyConflicts`. Stage A: a stale policy revision is rejected (vendor session mode); an already `Ready` tool is stage B | `Done` |
| `I-E10` | `interaction §11` | `release` | `nightly` | `TestIE10DecideMutationProbes`. Stage A: `quality/probes/04-decide-hard-rule-bypass.patch` (step 1 of `§3.2`), `quality/probes/18-grant-scope-key-bypass.patch` (`grantScopeKey`). Partial: no probe patch for treating `Responder = nil` as headless | `In progress` |
| `I-E11` | `interaction §11` | `release` | `pr` | `TestInteractionListPendingAndTopicCarryFull256KiBDetail`, `TestOversizedRequestContentFailsTyped` | `Done` |
| `HA-E01` | `host-api §10` | `release` | `pr` | `TestConcurrentAppStartsShareOneHost`, `TestHostManagerStartsAbsentHost`, `TestSecondServeOnTheSameDataDirectoryFailsTyped`, `TestHostFileFromCrashedHostIsReplacedOnStart`, `TestAppReconnectsAndCatchesUpFromSequence` | `Done` |
| `HA-E02` | `host-api §10` | `release` | `pr` | `TestShortSocketPathStaysInTheDataDirectory`, `TestLongSocketPathFallsBackToTheRuntimeDirectory`, `TestSocketDirectoryAndSocketAreOwnerOnly`, `TestPeerCredentialMismatchIsRejected` | `Done` |
| `HA-E03` | `host-api §10` | `release` | `pr` | `TestAppReconnectsAndCatchesUpFromSequence`, `TestEphemeralCursorCatchUpFromTheBuffer`, `TestDurableTooOldCursorGetsSnapshot`, `TestDurableSlowClientGetsResyncAndThePublisherNeverBlocks`, `TestHostconnReloadsTheSnapshotOnResyncOrNewIncarnation` | `Done` |
| `HA-E04` | `host-api §10` | `release` | `pr` | `TestVendorRetrySameCommandIDReturnsTheSameSession`, `TestIE07DuplicateCommandIDReturnsTheSameResult`, `TestStartSessionRetryAttachesToTheOperationOnARealHost` | `Done` |
| `HA-E05` | `host-api §10` | `release` | `pr` | `TestProtoGate`, `TestDialVersionMismatchIsTyped`, `TestVersionMismatchMessageNamesTheVersions`, `TestManagerFreezesOnAnIncompatibleHost` | `Done` |
| `HA-E06` | `host-api §10` | `release` | `pr` | `TestNoProtoLeak`, `TestLeaks`, `TestAllowedImporters` | `Done` |
| `HA-E07` | `host-api §10` | `release` | `pr` | `TestAppStateRestoresEveryWindowWhileTheVaultIsLocked`, `TestAppStatePutRejectsAStaleRevision`, `TestAppStateRejectsInvalidOrOversizedDocuments`, `TestAppStateDeniesNonAppPrincipals`, `TestHigherSchemaFallsBackAndKeepsTheStoredDoc`, `TestLayoutRestoresAcrossRestartWhileVaultLocked` | `Done` |
| `HA-E08` | `host-api §10` | `release` | `pr` | `TestChannelHostAPIPagesPostsAndLocksMetadataOnly`, `TestChannelHostAPIDeniesSessionPrincipal` | `Done` |
| `HA-E09` | `host-api §10` | `release` | `pr` | `TestReadWorkspaceFileConfinesToRegisteredRoots`, `TestWorkspaceFileClientMethodsOverRealSocket` | `Done` |
| `CL-E01` | `cli §9` | `release` | `pr` | `TestHostManagerStartsAbsentHost`, `TestConcurrentAppStartsShareOneHost`. Stage A, covered by `DA-E08` | `Done` |
| `CL-E02` | `cli §9` | `release` | `pr` | `TestLeaseZeroArmsGraceShutdown`, `TestNewLeaseInsideGraceCancelsShutdown`, `TestServeExitsAfterLastLeaseIdleGrace`, `TestServeArgumentsSelectOnlyAppIdleGrace`. Stage A, covered by `DA-E08` | `Done` |
| `CL-E03` | `cli §9` | `release` | `pr` | `TestGracefulStopRunsTheF92StopOrderForEverySession`, `TestGracefulDrainCancelsPendingToolApproval`, `TestStartHostDrainsThroughStoreCloseLast`, `TestPausedAccountReopensAtResetsAtAndDispatchesQueuedTasks`, `TestFailureUXRequestWindowCloseInterceptsALiveTurn`. Stage A host part only (orderly shutdown, app close prompt per `DA-E08`); the last-TUI prompt is stage B. Partial: no test that a run waiting on budget survives a restart | `In progress` |
| `CL-E04` | `cli §9` | `release` | `pr` | `TestVersionAdviceHonorsOtherClients`, `TestOlderBusyHostIsReportedWithoutShutdown`, `TestManagerFreezesOnAnIncompatibleHost`, `TestDialVersionMismatchIsTyped`. Stage A, covered by `HA-E05` | `Done` |
| `CL-E09` | `cli §9` | `release` | `pr` | `TestServeExitCodeForSignals`, `TestSecondServeOnTheSameDataDirectoryFailsTyped`, `TestRunUntilStoppedCloseErrorExitsFailure`, `TestServeUsageRejectsUnknownSubcommand`. Stage A part only (`fugue serve` exit codes, `cli §5.2`); the other commands and `--json` are stage B | `Done` |


## F11.7b team UI evidence

- DA-E07 performance: `TestTeamTreeLayoutBudget` (manual run with the `perf` build tag). The
  test measures the p95 layout time for a 12-agent tree during continuous updates.
- DA-E11 Chat/Team switch: `TestTeamSwitchPreservesChatAndComposer`.
- DA-E13 Channel: `TestTeamChannelThreadsRoutingAndInbox`,
  `TestTeamChannelRestoresUnreadPositionFromLayout`, `TestWorkerPassesThreadAndExplicitTargetToHost`,
  and `TestUserPostRoutesByMentionAndThread`.
