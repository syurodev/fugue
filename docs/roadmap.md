# Roadmap

The living work board of the repository. It answers: what to do, in what order, based on which
design, what counts as done, and where each item stands. This page **does not** change the
design.

Last updated: 2026-11-04 (F2.7 PR #44 opened for review).

## 0. Context

- **Product:** Fugue by SynX. This repository is the **all-in-one Go core**: one host process
  (`fugue serve`) owns the data directory, the vault, the store and every service; the desktop
  shell (Electron) and other clients talk to it through the Host API.
- **DevKit (Wails)** is frozen at its current release and no longer developed. This repository
  **does not** import or depend on `syurodev/dev-kit`.
- **Goal of this phase:** the native harness (stage A of the core harness design). The
  dependencies the harness needs are ported **first**.
- **Design** lives in the base-doc repository under `content/projects/dev-kit/core-harness/`.
  The tables below cite pages by name, for example `state-machine §3`. The design was written
  for DevKit, so its path names (`internal/...`, `devkit.db`, `AppService`) are superseded by
  this repository's ADRs; semantics stay the same. Real semantic differences are listed in
  [§7](#7-needs-decision).
- **No harness code is carried over** from dev-kit (`internal/harnessruntime`,
  `internal/execstore`, `internal/hostapi` from PRs #412 to #420). The harness is written from
  scratch.
- **Data starts clean:** data directory `os.UserConfigDir()/Fugue/`, new schema. Importing
  DevKit data is a separate, later item.
- **Stage A** is defined by `activation` v0.13 (Q-02): native only, the owner dogfoods it, Linux
  and macOS, a CLI/TUI client, Sign in with ChatGPT as the only provider, one release gate.
  Items marked *Stage B* in their notes are outside stage A and do not start before it.

## 1. Rules for the implementing agent

1. **Read before working:** `AGENTS.md`, [quality gates](quality-gates.md), the relevant ADRs in
   `docs/adr/`, and exactly the design pages the item points to.
2. **Work only on items whose design is settled.** Items in `Awaiting design` do not start.
3. **Never change design or gates on your own.** When something is wrong, missing or
   contradictory, move the item to `Needs decision`, record the question in
   [§7](#7-needs-decision), and take another unblocked item.
4. **Roadmap work: one item, one branch, one PR** into `main`, branch `<type>/<id>-<slug>` (for
   example `feat/f2.1-kernel-types`). A change outside the roadmap (bug, chore, docs) is also
   allowed as its own branch and PR, without an item ID. Never stack a PR on an unmerged PR.
   Keep PRs under roughly 600 changed lines, excluding generated code, golden files and
   testdata.
5. **Done** means: merged into `main`, every CI gate green, and the tests in the Evidence column
   exist and pass. `TestRoadmapEvidence` checks this.
6. **Update this page in the same PR:** status, PR link, evidence test names, one log line.
7. **Port from DevKit** under [§3](#3-porting-rules).

**Statuses:** `Not started` · `In progress` · `In review` · `Done` · `Blocked` (name the
blocking ID) · `Needs decision` · `Awaiting design`.

## 2. Phase overview

| Phase | Goal | Exit |
| --- | --- | --- |
| F0 | Repository foundation: names, layout, every quality gate, CI, platform primitives | `task check` green on a nearly empty repository; every gate has a negative test |
| F1 | Port dependencies: crypto, vault, storage and data-directory lock, credentials, interaction, sandbox, snapshot | Each package passes the same gates as new code; vector tests prove behavior is preserved |
| F2 | Headless vertical slice: kernel, codec, execution store, coordinator, faux model, fake read tool, continuation | Trace A reproduced exactly; replay calls neither model nor tool |
| F3 | Recovery, retry, timers, budgets and fault injection | Traces B and C; every row of the validation criteria has a test |
| F4 | Context engine and the OpenAI adapter with Sign in with ChatGPT | Evidence tables of the context engine and model adapter §11 |
| F5 | Host API (`fugue serve`), input coordination and workspaces | Evidence tables of the Host API and input coordination; `workspace §9` |
| F6 | Tools and interaction | Evidence of `interaction §11`, `sandbox-runtime §12`, `shell-job §8`, `content-safety §7`, `file-tools §11`, `ask-plan §7`, `tasks §12` |
| F7 | CLI/TUI client | Awaiting design H-45 |
| F8 | Stage A release | Evidence of `activation §5` |

## 3. Porting rules

Applies to all code copied from `syurodev/dev-kit`.

1. Copy **only what is needed**, one small package at a time; one PR per package.
2. Record the source in `docs/ports.md`: original path, dev-kit commit SHA, what was dropped or
   changed.
3. Remove every DevKit dependency: Wails, `bridge`, `app`, `AppService`, `DEVKIT_*` variables,
   the DevKit name.
4. Ported code passes **exactly the same gates as new code**: lint, coverage, mutation, `nolint`
   ratchet. Never add `nolint` to get a port through.
5. Preserve behavior with **vector tests**: before changing anything, write tests that capture
   the original code's output (for example ciphertext for a fixed key and nonce, a generated
   sandbox profile); those tests must still pass after the port.
6. Any change in behavior or format needs an ADR.
7. `crypto`, `vault` and `sandbox` PRs need the user's approval.

## 4. Work items

### F0 — Foundation

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F0.1 | Repository name, Go module path, LICENSE, README | dev-kit Fugue naming ADR | Q-01 | `go.mod` has the canonical module path; LICENSE present | Done | [#1](https://github.com/syurodev/fugue/pull/1) | Q-01 answered 2026-11-01 |
| F0.2 | Layout and layering ADR (this repository's G-01): package tree, layer table, owner of every SQL table, initial third-party dependencies | `architecture`, `host-api §4`, `storage-security` | F0.1 | ADR `docs/adr/0001-package-layout.md` approved by the user | Done | [#2](https://github.com/syurodev/fugue/pull/2) | ADR approved by the user 2026-11-01; the [§5](#5-proposed-layout) sketch is superseded where the ADR differs |
| F0.3 | Toolchain: `go`/`toolchain`, `tool` directives, `Taskfile.yml` (`check`, `fix`, `test`, `lint`), `.golangci.yml` per gates §2–3 | quality-gates §1–3, §7 | F0.2 | `task check` green; every enabled linter has a violating fixture that is caught: `TestLintFixtures`, `TestEveryEnabledLinterHasAFixture`, `TestFormattersFlagUnformattedFiles`; module gates: `TestTidyGate`, `TestVerifyGate`, `TestGovulncheckGate`, `TestLicenseGate`, `TestTaskfileRunsEveryGate`; tool gates: `TestDeadcodeGate`, `TestFuzzGate`, `TestMutationGate`, `TestEvaluate` | Done | [#4](https://github.com/syurodev/fugue/pull/4) | `cmd/fuguegate` carries the gates no tool covers (coverage, fuzz, mutation, deadcode). `fuguevet` and `arch` steps join `task check` in F0.4 and F0.5, `proto` in F0.7. PR is above the 600-line guideline because it carries the gate self-tests |
| F0.4 | `tools/fuguevet`: `kernelpure`, `nobarego`, `secretlog`, `todoref`, `noprotoleak` | quality-gates §4 | F0.3 | `analysistest` valid and violating cases for each analyzer: `TestKernelPure`, `TestNoBareGo`, `TestSecretLog`, `TestTodoRef`, `TestNoProtoLeak`, `TestLeaks`, `TestInTree`; the binary registers all five: `TestVettoolRunsEveryAnalyzer` | Done | [#5](https://github.com/syurodev/fugue/pull/5) | `vet` step added to `task check`. Q-08 records how `secretlog` reads the gate wording |
| F0.5 | `internal/arch`: every test in quality-gates §5, ratchets in §8 | quality-gates §5, §8 | F0.2, F0.3 | Each rule has a clean and a violating mini-repository under `internal/arch/testdata`: `TestLayerGraph`, `TestTableOwnership`, `TestPackageDocs`, `TestGoleakEverywhere`, `TestDependencyAllowlist`, `TestErrorCodesPublished`, `TestRoadmapEvidence`, `TestFileSize`, `TestNoPlaceholderCode`, `TestRatchets` | Done | [#6](https://github.com/syurodev/fugue/pull/6) | `TestRoadmapEvidence` reads this page | `TestRoadmapEvidence` reads this page. Error-code registry convention in Q-09 
| F0.6 | PR and nightly CI, `CODEOWNERS`, `gate-guard`; the user enables branch protection | quality-gates §9–10 | F0.3–F0.5 | A trial PR violating each gate is blocked (the table in `docs/github-setup.md` §5, run after the owner enables the ruleset). Self-tests: `TestPRWorkflowRunsEveryRequiredJob`, `TestNightlyWorkflowRunsTheLongJobs`, `TestActionsArePinnedToCommits`, `TestActionlintGate`, `TestCommitHookIsInstalledByTaskHooks`, `TestGateGuard`, `TestLintTitle`, `TestLintMessage`, `TestCommitMsgHookReadsTheFileAndIgnoresComments`, `TestGateFilesMatchTheDocumentAndCodeowners` | Done | [#9](https://github.com/syurodev/fugue/pull/9) | Branch protection is a GitHub setting the user changes. Also: a `commit-lint` job rejects any PR commit or PR title containing `Co-Authored-By` and checks that the PR title is a Conventional Commit (no item ID required); a local `commit-msg` hook, installed by `task hooks`, applies the same rules | Trial PRs #11 to #22 (draft, closed unmerged) each failed the expected check and were `BLOCKED` by the ruleset: fmt, lint, vet, arch, test, coverage, deadcode, mod, proto, gate-guard (no label), commit-lint (trailer) and commit-lint (title `WIP: ...`). Not exercised live: the label applied by someone other than the owner (one account only; `TestGateGuard` covers it). Q-10 open 
| F0.7 | `proto/fugue/` skeleton with `buf` (lint, breaking, Go and TS generation for Connect), no services yet | `host-api §4, §8` | F0.2 | `proto` job green; every step has a violating fixture: `TestProtoGate` (lint, format, breaking change, stale, missing and leftover generated files), `TestProtoBreakingIsSkippedOnlyWhenBaseHasNoProto` | Done | [#7](https://github.com/syurodev/fugue/pull/7) | | `task generate` writes Go to `internal/hostapi/internal/pb` and TypeScript to `gen/ts`; TypeScript uses the pinned remote plugin `buf.build/bufbuild/es`, so `task proto` needs network. `hello.proto` holds only the Hello messages named in host-api §8; no service yet 
| F0.8 | Platform primitives: `clock` (real and fake), `ids`, coded errors and registry, `redact.Secret`, `supervise`, `config` | quality-gates §3–4 | F0.3 | Tests for each primitive; the fake clock drives timers: clock `TestFakeTimersFireInDeadlineOrder`, `TestFakeStopAndReset`, `TestSleepWaitsForTheClock`; ids `TestUUIDv7SortsByCreationTime`, `FuzzIsUUID`; errcode `TestExposeNeverLeaksDetail`, `TestRegistryCoversEveryCode`; redact `TestSecretNeverPrintsItsValue`, `TestHandlerMasksSecretLookingKeys`; supervise `TestStopCancelsTheTaskContext`, `TestPanicIsRecoveredWithoutItsValue`; config `TestLoadDefaults`, `TestLoadRejectsBadValues` | Done | [#8](https://github.com/syurodev/fugue/pull/8) | | `config` is `internal/config` per ADR 0001. Gate fixes found while writing real code are in the gate change log (quality-gates §11) 

### F1 — Port dependencies

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F1.1 | `crypto`: XChaCha20-Poly1305 AEAD, Argon2id KDF, envelope, keyring | `storage-security §4` | F0.8 | Vector tests from dev-kit `internal/crypto`: `TestSealMatchesTheOriginalVector`, `TestSealOfEmptyPlaintextMatchesTheOriginalVector`, `TestDeriveKeyMatchesTheOriginalVectors`, `TestNewKeyringMatchesTheOriginalVector`, `TestTheOriginalKeyringStillUnlocks`; envelope fuzzing: `FuzzEnvelopeJSON`, `FuzzSealOpenRoundTrip` | Done | [#24](https://github.com/syurodev/fugue/pull/24) | User approval. Provenance and the few changes are in `docs/ports.md`; `docs/dependencies.md` gains `golang.org/x/crypto`, so the PR needs `gate-change-approved` |
| F1.2 | `vault`: Master Password, locked on every process start, `SealRecord`; observation API (snapshot, revision, subscription, lock guard) | `storage-security §8` | F1.1 | Unlock before recovery, lock while in use, slow subscriber: `TestEveryProcessStartIsLocked`, `TestAnUnlockedEventNeverPrecedesAUsableKey`, `TestNothingSucceedsAfterLockReturnsWhileWorkIsInFlight`, `TestAGuardRunsBeforeTheKeyIsZeroedAndWhenNothingCanUseIt`, `TestASlowSubscriberIsMarkedLaggingAndResyncs`; vector: `TestARecordSealedByTheOriginalCodeStillOpens` | Done | [#26](https://github.com/syurodev/fugue/pull/26) | User approval. Observation API written fresh from the design. The keyring is persisted through the `KeyringStore` port with a memory implementation; the SQL implementation over `vault_meta` lands with F1.3 because the table is created by storage's migrations. Provenance in `docs/ports.md` |
| F1.3 | `storage`: open SQLite (`modernc.org/sqlite`), pragma checks, migration framework, `Fugue/` data directory, `fugue.lock`, incarnation | `storage-security §2–3, §9` | F0.2, F0.8 | Wrong pragma rejected; two processes, path aliases, crash then reacquire the lock: `TestOpenRestrictsPermissionsAndSetsDurabilityPragmas`, `TestSQLiteDoesNotCreateUnprotectedWALSidecars`, `TestVerifyPragmasRejectsWrongJournalModeSynchronousAndForeignKeys`, `TestMigrateIsIdempotentAndContiguous`, `TestTwoProcessesCannotHoldTheDataDirectoryLock`, `TestPathAliasesAndSymlinksContendOnTheSameLock`, `TestCrashReleasesTheLockAndAdvancesHostIncarnation`, `TestNetworkFilesystemIsRejected`, `TestAcquireHostIncarnationRequiresHeldLockAndMonotonicallyAdvances`, `TestSQLKeyringStorePersistsAcrossReopen` | Done | [#29](https://github.com/syurodev/fugue/pull/29) | Provenance in `docs/ports.md`; `docs/dependencies.md` gains `modernc.org/sqlite` and `golang.org/x/sys`, so the PR needs `gate-change-approved` |
| F1.4 | `credentials`: model provider accounts (API key in the vault, Sign in with ChatGPT tokens), `PickAccount` | `model-adapter §7, §11` | F1.2, F1.3 | No secret in logs or errors; account choice follows rate limits: `TestPickAccountMatchesTheOriginalVectors`, `TestAChatGPTPlanAccountNeverFailsOver`, `TestAChatGPTPlanAccountIsNeverAFailoverTarget`, `TestFailoverStaysWithinOneProvider`, `TestNoSecretReachesTheDatabaseFormattingOrErrors`, `TestSecretsFailClosedWhileTheVaultIsLocked`, `TestARowEditedInTheDatabaseCannotBeOpened`, `TestUsageIsMergedByLabelAndStaleReadingsAreIgnored` | Done | [#31](https://github.com/syurodev/fugue/pull/31) | Only what the model adapter needs. Provenance in `docs/ports.md`; migration `0002_credential_accounts.sql` creates the table the ADR assigns to this package |
| F1.5 | `interaction`: in-memory requests per incarnation, receipts, answer-once | `tool-system §5`, D-16 | F0.8 | An old request cannot be answered after restart; a duplicate answer is rejected: `TestAnOldRequestCannotBeAnsweredAfterRestart`, `TestADuplicateAnswerIsRejected`, `TestEnsureReadsBackByOperationIDAndRejectsConflictingSpecs`, `TestAReceiptCanBeClaimedOnlyOnceAndMatchesItsBinding`, `TestSelectionRequestsAcceptOnlyOfferedOptionsAndNeverAuthorizeExecution`, `TestImportantRequestsAlwaysAskAndRefuseSessionGrants`, `TestVaultLockInvalidatesPendingRequestsGrantsAndReceipts`, `TestNilResponderAndHeadlessFailClosed` | Done | [#27](https://github.com/syurodev/fugue/pull/27) | Full API awaits H-24. Provenance in `docs/ports.md` |
| F1.6 | `sandbox`: policy, seatbelt, bwrap and landlock backends, process runner | `tool-system §6` | F0.8 | Vector tests of generated profiles; smoke tests on macOS and Linux: `TestSeatbeltProfileMatchesTheOriginalVectors`, `TestBwrapArgsMatchesTheOriginalVector`, `TestSeatbeltProfileRulesAndValidation`, `TestBwrapArgsNestedBindsAndResolvConf`, `TestPolicyValidationAndDenyWriteRules`, `TestRunnerAndProcLifecycle`, `TestBwrapBackendUnitAndSmoke`, `TestRestrictSelfSkippedOrApplies`, `TestSeatbeltBackendUnitAndSmoke` | Done | [#30](https://github.com/syurodev/fugue/pull/30) | User approval; per-session jail awaits H-25. Provenance in `docs/ports.md` |
| F1.7 | `snapshot`: checkpoint shadow git, interface per the design | `tool-system §9` | F0.8 | Two separate quotas; restore touches only the tool's diff: `TestToolSnapshotQuotaNeverEvictsTurnSnapshots`, `TestSoftCapPrunesToolSnapshotsBeforeTurnSnapshots`, `TestRestoreToolTouchesOnlyTheToolsDiff`, `TestRestoreKeepsSymlinksAndExecutableBit`, `TestRestoreIsByteExactUnderProjectGitattributes`, `TestRestoreRefusesToWriteThroughASymlinkedDirectory`, `TestRestoreReportsAndCanOverrideAConflict`, `TestApplyRefusesAStalePlan`, `TestMaintainDropsOrphanRefsAndKeepsLiveTrees` | Done | [#28](https://github.com/syurodev/fugue/pull/28) | Provenance in `docs/ports.md` |

### F2 — Headless vertical slice

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F2.1 | Kernel: command and event types, `decide` and `evolve` for run, step, model and tool invocation/attempt; derived lifecycle, phase and `waitReasons` | `state-machine §1–7`, `event-catalog §1–5` | F0.4, F0.8 | Tables of valid and rejected transitions | Done | [#33](https://github.com/syurodev/fugue/pull/33), [#34](https://github.com/syurodev/fugue/pull/34) | Both parts merged with green CI (Q-12) |
| F2.1a | Kernel: 54 event types, 27 minimal command types, `State`, total pure `evolve`, derived lifecycle, phase and `waitReasons`, rejection types/codes | `state-machine §1–2`, `event-catalog §1–5` | F0.4, F0.8 | `TestEvolvePurity`, `TestEvolveQuiesceLifecycle`, `TestEvolveInterruptionCauses`, `TestEvolveIdempotenceAndMonotonicity`, `TestEvolveRunAndStepCoverage`, `TestDeriveLifecyclePrecedence`, `TestDerivePhaseFrontier`, `TestDeriveWaitReasons`, `TestCommandNames`, `TestEventNamesAndEnvelopes`, `TestTypedNilEvents` | Done | [#33](https://github.com/syurodev/fugue/pull/33) | `kernelpure` green; coverage 98.1%; mutation 98.9%; scope per Q-12. Note: PR exceeded the ~2,000-line budget (agent underestimated exhaustruct fixtures for 54 events/27 commands, linter helper splits, and mutation test assertions, and failed to pause before PR creation; rule acknowledged: agent must stop and consult whenever thresholds are reached). Review fixes: QuiescingAttempts guarded against ghost attempts and cleared on terminal attempt results; CauseID on RunInterruptedEvent prevents collisions without fallback; total pure evolve guards typed-nil events and nil map values; typed enums for outcomes/control dispositions/reasons; nil-safe EventEnvelope; events_step.go split to keep all files <= 500 lines; ToolCallPlanned idempotency |
| F2.1b | Kernel: `decide` for Trace A commands, rejection, no-op and dedup transitions | `state-machine §4–5`, `event-catalog §6–7` | F2.1a | `TestDecideTraceA`, `TestDecideRejectsInvalidTransitions`, `TestDecideEvidenceGuards`, `TestDecideAdmissionGuards`, `TestDecideDuplicateObservations`, `TestDecideConflictingDuplicates`, `TestDecideConflictingStream`, `TestDecideSequentialToolFrontier`, `TestDecideDeterministic`, `TestDecideDoesNotAliasPlans`, `TestDecideRejectsIdentityAndCursorCorruption`, `TestDecideDuplicateAfterAccountingObservation` | Done | [#34](https://github.com/syurodev/fugue/pull/34) | Scope per Q-12: host-bound facts; pure decision batches for runnable Trace A, no approval/retry/time budget. Durable command-ID receipts remain F2.3/F2.4 |
| F2.2 | Invariant checker SM-01 to SM-12; property tests over random command sequences | `state-machine §3` | F2.1 | `TestEverySnapshotInvariantRejectsAViolation`, `TestEveryHistoricalInvariantRejectsAViolation`, `TestInvariantsTraceA`, `TestRapidCommandSequences`, `TestHistoryChecksEveryBatch`, `TestApprovalResolutionBindsExactArguments`, `TestWindowAccountingRequiredBeforeFinish`, `TestRetryPreservesAttemptEvidence`, `TestUnknownControlClosurePreservesExposure`, `TestQueuedRebuildHasNoEffects`, `TestMetadataCloseDisposesInterruptedModel` | Done | [#37](https://github.com/syurodev/fugue/pull/37) | Q-06 approved; implementation plan: [F2.2](plans/f2.2-invariants.md) |
| F2.3 | Canonical codec v1, receipt deduplication | `storage-security §5`, `event-storage-recovery §5` | F2.1 | `TestCodecDuplicateKeys`, `TestCodecDisallowUnknownFields`, `TestCodecNumberValidation`, `TestCodecMalformedAndTruncated`, `TestCodecDeterministicEncoding`, `TestCodecRoundTrip`, `TestCommandRoundTripAllTypes`, `TestDecodeCommandErrors`, `TestReceiptDeduplicationSameCommandID`, `TestReceiptDedupDifferentPayload`, `TestReceiptDedupDifferentKind`, `TestReceiptDedupDifferentPrincipal`, `TestReceiptDedupDifferentTarget`, `TestCodecVersionMismatch`, `TestReceiptDedupMetadataMatch`, `TestReceiptDedupMetadataConflict`, `TestReceiptDeduplicationVaultLockedFailClosed`, `TestReceiptDiagnosticsNoSecrets`, `TestRegressionDuplicateKeyDiagnostics`, `TestRegressionUnknownFieldDiagnostics`, `TestRegressionNumericTokenDiagnostics`, `TestRegressionEncoderRejectsInvalidUTF8`, `TestRegressionRejectsSemanticDuplicateField`, `TestRegressionIntegerPrecisionPreservedInAny`, `TestRegressionNilCommandAndNullRejected`, `TestRegressionUTF8DepthDoesNotFailOpen`, `TestRegressionNestedStructBinding`, `TestRegressionNilStructFieldRoundTrip`, `TestRegressionEmbeddedStructRoundTrip`, `TestRegressionTraversalDepthLimitsEncoder`, `TestRegressionTraversalDepthLimitsDecoder`, `TestRegressionContainerEdgeCases`, `TestRegressionEmbeddedDeclaredFirstDominance`, `TestRegressionChildDeclaredFirstDominance`, `TestRegressionFloatTargetsRejected`, `TestRegressionNonStringMapsRejected`, `TestCanonicalFieldTagDominance`, `TestCanonicalContainerDepthRoundTrip`, `TestCanonicalIgnoredFields`, `TestCanonicalDeepEmbeddingRoundTrip`, `TestCanonicalRecursiveEmbedding`, `TestCanonicalCyclesRejected`, `FuzzCanonicalJSON`, `FuzzCanonicalRoundTrip`, `FuzzDecodeCommand` | Done | [#38](https://github.com/syurodev/fugue/pull/38) | Canonical codec v1 and receipt deduplication (F2.3); pure in-memory, execution store persistence left to F2.4 |
| F2.4 | Execution store: `exec_*` tables, append-only triggers, one transaction for event, payload, projection, receipt and admission | `storage-security §2–3, §6`, `event-storage-recovery §5` | F1.3, F2.3 | `TestExecutionBatchAtomicAcrossReopen`, `TestExecutionCrashRollsBackIncompleteBatch`, `TestCommitLostAcknowledgementCommitted`, `TestExecutionReceiptSurvivesRestart`, `TestExecutionReceiptConflictWritesNothing`, `TestExecutionReplayMatchesProjections`, `TestExecutionEventCodecCoversKernelRegistry`, `TestExecutionStoreRejectsUnsafePragmas`, `TestDirectAdmissionAndFoldEdgeCases`, `FuzzExecutionEventDecode`, `TestRegressionR1_IncrementalStateAndProjections`, `TestRegressionR2_CanceledAppendRollsBack`, `TestRegressionR3_QueueAuthorityCannotAdmit`, `TestRegressionR4_MetadataOnlyPlaintextBoundary`, `TestRegressionR5_DuplicateAfterPreflightNoLeak`, `TestRegressionR6_CanonicalTranscriptAdmissionOrder`, `TestRegressionR7_ProtectedDedupUsesSessionBinding`, `TestRegressionR8_ResolveCommitConnectionVerification`, `TestRegressionInitialAdmissionRejectsForeignHost`, `TestRegressionQueueCannotStartStep`, `TestRegressionQueueAuthorityRejectsExecution`, `TestRegressionQueueCannotWriteAdmittedRun`, `TestRegressionQueueRejectsNonzeroEventToken`, `TestRegressionSameBatchInputExactlyOnce` | In review | [#42](https://github.com/syurodev/fugue/pull/42) | Q-13 A approved: includes physical payload/key/purge support schema and sealed-record persistence; [implementation plan](plans/f2.4-execution-store.md). Implementation PR #42 |
| F2.5 | Protected payloads: session key wrapped with `SealRecord`, AEAD, size limits, crypto-shred | `storage-security §4, §7` | F1.2, F2.4 | Crypto-shred; 1 MiB, 64 MiB and 1 GiB limits | Not started | | Q-13 A: physical support schema lands in F2.4; crypto/key/vault/purge lifecycle, limits and G-03 security evidence remain here |
| F2.6 | Per-session coordinator: admission row, minimal queue, worker reservation, commit before handoff, read-back on unknown commit, durable timers | `event-storage-recovery §2–3, §7`, `architecture §3, §6` | F2.4 | An unknown commit never dispatches twice; a stale worker is fenced | Not started | | |
| F2.7 | Faux model provider: text, thinking, tool calls, every error class, cut streams | `model-adapter §2–3, §9` | F2.1 | `TestErrorClassRetryClassification`, `TestScriptEmitsOrderedContentAndTerminalEvidence`, `TestScriptsRepeatInDeclaredOrderAndClassifyFailures`, `TestCutPointsCloseWithoutInventingTerminalOutcome`, `TestCancellationStopsStreamAndClosesIt`, `TestScriptFailureIsOwned`, `TestReceivedFailureDoesNotChangeNextCall` | In review | [#44](https://github.com/syurodev/fugue/pull/44) | Shared minimal contract types; the coordinator owns its consumer port per ADR 0001 |
| F2.8 | Fake read tool (`pure_read`) on the `Prepare`/`Execute` contract | `tool-system §2` | F2.1 | Full check, ready, dispatch, result chain | Not started | | |
| F2.9 | After-step rules, `maxSteps` with `Waiting(step_budget)` and `BudgetExtended`, one automatic continuation on `length`, loop reminders | `continuation §2–4` | F2.1, F2.3 | Evidence in continuation §8 (except goal) | Not started | | Loop reminders need F4.5 |
| F2.10 | **End-to-end slice**: start, model, tool, model, finish (trace A); durable; replay without side effects | `event-catalog §7` | F2.2–F2.9 | Trace A has exactly 22 events; replay makes 0 model and tool calls | Not started | | Main F2 milestone |

### F3 — Recovery and fault injection

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F3.1 | Restart classification: Queued, `ToolCheckReset`, dispatching becomes unknown, vault locked, payload lost | `state-machine §8`, `event-storage-recovery §7` | F2.10 | Trace B; an unknown effect never reruns | Not started | | |
| F3.2 | Stop intent, cancel, quiesce, `AcceptUnknown`, `CloseRunWithoutPayload`, `SelectRunContext` | `state-machine §6`, `event-storage-recovery §3` | F3.1 | Trace C | Not started | | |
| F3.3 | Model retry: `Planned` attempt, retry intent and timer written in one batch before waiting; timer elapsed only opens dispatch | `event-catalog §3`, `model-adapter §3` | F2.10 | A crash while waiting creates no new attempt and charges nothing again; a stop while waiting cancels the attempt and the timer | Not started | | |
| F3.4 | Active-time budget, tranches, crash mid-tranche | `state-machine §7` | F2.6 | Downtime is not charged; conservative charge on resume | Not started | | |
| F3.5 | Fault injection suite per the validation criteria table | `decisions-and-validation` (validation criteria) | F3.1–F3.4 | Every row of the table has a test; guards meet the mutation threshold | Not started | | Closes G-02 |

### F4 — Context engine and model adapters

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F4.1 | Neutral model contract, error classification | `model-adapter §2–3` | F2.10 | The faux provider covers every error class | Not started | | |
| F4.2 | Anthropic adapter (API key), converter, catalog, usage | `model-adapter §4–8, §10` | F4.1, F1.4 | Golden streams; signed thinking; SDK control conditions re-verified | Not started | | *Stage B* (activation A-02, model-adapter M-11) |
| F4.3 | OpenAI adapter with Sign in with ChatGPT: registration, token refresh, converter for the plan's request rules, per-account model list | `model-adapter §11`, `activation §2` | F4.1, F1.4 | Error table §11.4 has tests; no automatic failover on a ChatGPT plan; spike §11.7 recorded with a real account | Not started | | First and only real adapter of stage A (M-11). The API-key path is *Stage B*. Login and plan text are shown by the CLI (F7.1) |
| F4.4 | Account failover from rate limits | `model-adapter §7` | F4.2 | All accounts limited leads to `Waiting(retry_timer)` | Not started | | *Stage B*: a ChatGPT plan never fails over (M-09) |
| F4.5 | Context engine: L0–L5, `ContextInjected`, `InstructionsLoaded`, rules R0–R3, token measurement | `context-engine §2–3, §5` | F2.10 | Deterministic assembly; patches leave the prefix unchanged | Not started | | |
| F4.6 | Pruning, compaction, language check | `context-engine §6` | F4.5, F4.1 | Context engine evidence table §9 | Not started | | |
| F4.7 | W tier: port `workspacememory`, pinned blocks, index, hash freshness, suggestions | `context-engine §4` | F4.5, F1.3 | Redaction; CAS on approval; INVALIDATE keeps history | Not started | | Port per §3 |

### F5 — Host API and input coordination

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F5.1 | `fugue serve`, `host.json`, `fugue.sock`, UID check, `Hello` and versioning | `host-api §3, §7, §8` | F0.7, F1.3 | Single host; socket permissions; versions | Not started | | |
| F5.2 | Event stream with sequence, snapshot, catch-up, `resync_required` | `host-api §6` | F5.1 | Disconnect and reconnect at the right sequence | Not started | | |
| F5.3 | Session, queue and interaction services over Connect, including `SetSessionPolicy` | `host-api §4–5`, `interaction §8` | F5.2, F2.10, F6.3 | `command_id` deduplication; `origin` derived from the principal; interaction evidence §11 for `ListPending`, `Answer` and two clients | Not started | | |
| F5.4 | Input coordination: durable queue, Steer, Interject, Stop returns items to the composer, `Continue`, `Inject` | `coordination` | F5.3, F3.2 | Coordination evidence table §10, except peers | Not started | | Peers dropped (Q-04 |
| F5.5 | Goal mode | `continuation §5` | F5.4, F6.1 | Goal evidence in continuation §8 | Blocked | | Waits for F6.1 |
| F5.6 | Workspaces and configuration: `internal/workspace`, `~/Fugue/` layout with `workspaces/<id>/`, named `rw`/`ro` roots, the `workspaces` table, four TOML layers with strict validation, reload at step boundaries, `policy_revision` bumps on policy keys, root changes applied at step boundaries | `workspace` | F5.1, F2.4, F6.3 | `workspace §9` | Not started | | Jail restarts on root changes land with F6.4; `fugue workspace` commands land with F7.1 |

### F6 — Tools and interaction

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F6.1 | Registry, descriptor, factory, result servant, binding revision | `tool-system §2–4, §10–11` | F2.10 | `ToolBindingRevised` only at step boundaries | Not started | | |
| F6.2 | Turn and tool snapshots wired into the harness | `tool-system §9` | F6.1, F1.7 | Restore touches only the tool's diff | Not started | | |
| F6.3 | Interaction policy and owner changes: `Decide` with approval modes and hard rules, `grantScope` grants, `question` kind, deny note, `exec_session_policy`, gate observations | `interaction §1–7, §9` | F6.1, F1.5, F2.4 | `interaction §11` except the Host API rows | Not started | | Changes the F1.5 contract (`interaction §4.2`), including `TestNilResponderAndHeadlessFailClosed`. May split into a policy PR and an owner PR. Env tag registry waits for the first tool with a tagged target (*Stage B*) |
| F6.4 | Sandbox runtime: `fugue jail-init` supervisor, host–supervisor channel, stop proof, lazy start and idle stop, `exec_jail_processes` and reaper, filtered env snapshot, Docker endpoint in `full-access` | `sandbox-runtime` | F6.1, F1.6, F6.3, F2.4, F5.6 | `sandbox-runtime §12` | Not started | | Linux daemon spike becomes evidence. Egress allowlist, Docker proxy and Keychain are *Stage B* (`sandbox-runtime §13`) |
| F6.5 | `ask_user` (1–4 questions with options) and `plan` (every mode, Plan mode exit through the user's answer, approved plan in `exec_session_policy`); `question` kind with `Questions[]` and `Answers[]`; `fugue answer` path for every interaction kind | `ask-plan` | F6.1, F6.3 | `ask-plan §7` | Not started | | Changes the F6.3 `question` contract (`ask-plan §4`) |
| F6.9 | Tasks and checklists: `internal/tasks` store sealed with vault records, `todo` steps, `tasks.*` tools, claim, verify gate in a one-off jail, context re-attach after compaction | `tasks` | F6.1, F6.3, F6.4, F6.7 | `tasks §12` | Not started | | `fugue tasks` commands land with F7.1. Multiple boards, custom workflows and sync are *Stage B* |
| F6.6 | `shell`, `shell.output` and `job.*` tools: per-command risk, grant scope for a single command, cwd, compact and full output with built-in filters, timeout to job, daemons | `shell-job` | F6.3, F6.4, F6.7 | `shell-job §8` | Not started | | |
| F6.7 | Content safety: `internal/secrets` detection (known values, token patterns, key-name assignments), session placeholder table in `exec_secret_tables`, reverse substitution limited to origin files, envelope with nonce, R0 paragraph, compaction summary and unverified memory in envelopes | `content-safety` | F6.1, F2.5 | `content-safety §7` | Not started | | Context engine wiring of the R0 paragraph and envelopes lands with the context engine items |
| F6.8 | File tools `read`, `grep`, `glob`, `edit`, `write`: read and write scope from the sandbox profile, multi-file `edit` batches applied all or nothing, multi-tier matching, seen versions, syntax check after write, content-loss guard, atomic writes, `fugue bench edit` with a shell-bypass rate | `file-tools` | F6.2, F6.3, F6.7, F5.6 | `file-tools §11` | Not started | | Code reading (H-19) is *Stage B*. The edit format is kept or changed from `fugue bench edit` results |

### F7 — Shell and UI

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F7.1 | CLI/TUI client `fugue`: sessions, interaction answers, vault, ChatGPT login, jobs, snapshots, `fugue tasks`, `fugue workspace`, every failure UX row | H-45 | F5.3 | — | Awaiting design | | The only client of stage A (activation A-06) |
| F7.2 | Electron shell and desktop session UI | H-44, H-33 | F5.1 | — | Awaiting design | | *Stage B*; may be a separate repository |

### F8 — Stage A release

| ID | Item | Design | Depends on | Evidence | Status | PR | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F8.1 | Stage A release: release evidence, upgrade and downgrade drills, recovery drills on Linux and macOS | `activation §4–6` | F2–F7 | Every item of `activation §5` with a revision | Not started | | Q-02 answered. "A stable" is the owner's recorded decision (activation §6) |

### After stage A

Stage B list (activation §7): Electron and desktop UI, OpenAI API key and Anthropic, account
failover, code reading, web, MCP and plugins, attachments, observability, Windows, packaging and
auto-update, other users, remote access. Also: import of DevKit data and the DevKit modules that
Fugue may rebuild (git, tasks, terminal, database, ssh, API client…). Each area needs its own
design and plan before it enters this board.

## 5. Proposed layout

Input to ADR F0.2, which is approved with the deviations it lists; where this sketch and
[ADR 0001](adr/0001-package-layout.md) differ, the ADR is authoritative.

```text
cmd/fugue/                 main: fugue serve and subcommands
internal/
  platform/                clock, ids, supervise, redact, errcode (stdlib only)
  config/                  typed configuration; the only reader of environment variables
  kernel/                  pure decide/evolve, commands, events, invariants (stdlib only, imports nothing of this module)
  codec/                   canonical codec v1
  crypto/  vault/          ported from DevKit
  storage/                 DB open, pragmas, migrations (DDL owner), data-directory lock, exec_host
  execstore/               SQL for the other exec_* tables, protected payloads
  coordinator/             per-session actor, dispatch, timers, scheduler
  contextengine/
  modeladapter/            neutral contract; anthropic/, openai/, faux/
  credentials/
  tools/                   registry, descriptors; concrete tools in subpackages
  interaction/  sandbox/  snapshot/  workspacememory/
  app/                     composition root and application services
  hostapi/                 Connect handlers; generated code in hostapi/internal/pb, importable only here
  arch/                    architecture tests
proto/fugue/<module>/v1/
tools/fuguevet/
quality/                   coverage and ratchet baselines
docs/                      roadmap, quality-gates, dependencies, ports, adr/
```

Dependency direction: `platform` ← `kernel` ← `coordinator` ← `app` ← `hostapi` ← `cmd`.
`execstore`, `modeladapter` and `tools` implement ports defined by `coordinator` and are wired
together only in `app`.

## 6. Gate progress

| Gate | Evidence | Items |
| --- | --- | --- |
| G-01 Package | ADR F0.2 approved; `TestLayerGraph` green | F0.2, F0.5 |
| G-02 Transition | Transition tables, invariants, property tests, fault injection | F2.1, F2.2, F3.5 |
| G-03 Storage | Atomicity, pragmas, triggers, crypto-shred, size limits, lock | F1.3, F2.4, F2.5 |
| G-04 Model and tool | Real adapters, real tools | F4.*, F6.* |
| G-05 Interaction | Design settled (`interaction`); evidence `interaction §11` | F1.5, F6.3, F5.3 |
| G-06 Compatibility | Fugue version compatibility: upgrade and downgrade drills (`activation §4`) | F8.1 |
| G-07 Activation | Stage A release evidence (`activation §5`) | F8.1 |

## 7. Needs decision

| ID | Date | Item | Question | Answer |
| --- | --- | --- | --- | --- |
| Q-01 | 2026-11-01 | F0.1 | Repository name and Go module path. Proposal: rename the repository to `fugue` (the target name in the Fugue naming ADR), module `github.com/syurodev/fugue`, unless a SynX GitHub organization exists. Which LICENSE? | Repository `fugue`, module `github.com/syurodev/fugue`; LICENSE Apache-2.0 (user, 2026-11-01) |
| Q-02 | 2026-11-01 | F8.1 | The activation design (H-36, H-37, G-06, G-07) assumes the native harness runs beside vendor sessions inside DevKit. Fugue is a new product: what does a stage A release require? | `activation` v0.13 (decisions A-01 to A-08): native only, owner dogfood, Linux and macOS, CLI/TUI client, Sign in with ChatGPT only, one release gate, "A stable" by the owner's decision (user, 2026-11-03) |
| Q-03 | 2026-11-01 | F1.3 | DB file name. The old ADR kept `devkit.db` only to avoid a migration; data now starts clean, so the proposal is `fugue.db` | `fugue.db` (user, 2026-11-02) |
| Q-04 | 2026-11-01 | F5.4 | Peer messages in the design go through the Agent Hub peer outbox, which this repository does not have. Proposal: drop peers from stage A; child sessions are already outside v1 | Peers dropped (activation A-03, user, 2026-11-03) |
| Q-05 | 2026-11-01 | Design | Drop H-43 (inventory of DevKit frontend logic) and the `AppService` ratchet, since DevKit is frozen | Dropped with H-22 and the prompt-site ratchet (activation A-03, user, 2026-11-03) |
| Q-06 | 2026-11-01 | F2.2 | `pgregory.net/rapid`, the property-test library named in quality-gates §6, is MPL-2.0, outside the allowed license list. Approve it, or name another library? | Approved by the user, 2026-11-02, for tests only; retain upstream notices and the full MPL-2.0 text in the third-party license inventory. Implementation target: rapid v1.3.0. Other MPL dependencies remain subject to separate approval. |
| Q-07 | 2026-11-01 | F0.3 | quality-gates §3 allows `panic` in `must*` helpers in `cmd`. `forbidigo` cannot scope by function name. Proposal: only files named `cmd/<name>/must.go` may call `panic`. Accept, or name another mechanism? | |
| Q-08 | 2026-11-01 | F0.4 | `secretlog` wording in quality-gates §4 is loose. Reading used: sinks are `slog`, `fmt`, `errors.New`/`Join` and test output (`t.Log`, `t.Error`, `t.Fatal`, `t.Skip`); a call into `internal/platform/redact` is the safe path; reported are `redact.Secret` values, byte slices or arrays named like key material (`key`, `dek`, `kek`, `nonce`, `salt`, `seed`) and any non-numeric value named like a secret (`token`, `secret`, `password`, `apikey`, `credential`, `privatekey`); numbers and booleans are exempt so token counts can be logged. Accept, or tighten (for example every `[]byte`)? | |
| Q-09 | 2026-11-01 | F0.5 | quality-gates §5 says `TestErrorCodesPublished` checks codes "returned through the Host API" against a public registry, but names neither. Convention used: codes are string constants of type `Code` in `internal/platform/errcode` (F0.8); the registry is `docs/error-codes.md` with a Public table and a Hidden table (each hidden code has a reason); every declared code must be in exactly one table and the page may not name an undeclared code. Accept? | |
| Q-10 | 2026-11-01 | F0.6 | quality-gates §10 lists eight gate-defining files. `cmd/fuguegate/**` (coverage, fuzz, mutation, deadcode, proto, commit-lint and gate-guard logic), `.github/CODEOWNERS`, `.githooks/**`, `AGENTS.md`, `docs/error-codes.md` and the `tool` and `require` lines of `go.mod` also decide what passes. `CODEOWNERS` already assigns the first five to the owner; `gate-guard` follows §10 exactly. Add them to §10 so the label is required for them too? | |
| Q-11 | 2026-11-01 | F1.2 | `fuguegate mutation` gives gremlins no `-count=1`, so when the package's tests are cached (a second `task check` run without a test change) gremlins measures a baseline of about 0.1 s, its timeout becomes 50x that, and slow but healthy mutants are counted as timed out and fail the score. Cold runs, such as CI, pass. Proposal: run gremlins with `GOFLAGS=-count=1` in `cmd/fuguegate/mutation.go`. This edits a gate file, so it needs your label | |
| Q-12 | 2026-11-02 | F2.1 | Roadmap F2.1 references state-machine §1–7, but F3.1–F3.4 already own stop/cancel/quiesce, AcceptUnknown, CloseRunWithoutPayload, SelectRunContext, recovery, retry and time tranches. How should F2.1 scope be bounded and delivered? | F2.1 is split into two PRs: F2.1a delivers all 54 event types, 27 minimal commands, State, total pure evolve, derived projections (lifecycle, phase, waitReasons), and rejection types/codes; F2.1b delivers decide for Trace A commands (with reject/no-op/dedup). decide for stop, recovery, retry, timer, AcceptUnknown, CloseRunWithoutPayload, SelectRunContext, and ResumeRun is deferred to F3.x. F2.1 becomes a composite row marked Done when both F2.1a and F2.1b merge. (user, 2026-11-02). Event clarifications added in F2.1a: (1) RunInterruptedEvent carries CauseID to match CauseIDs in RunRecoveryCleared and RunClosureConfirmed, ensuring distinct incidents of the same cause type do not collide; decide in F2.1b must supply a unique CauseID, and events with empty CauseID are ignored by evolve; (2) QuiescingAttempts is guarded against ghost attempts and cleared on terminal attempt results; decide in F2.1b must reject quiesce for unknown attempts; (3) typed-nil events and nil map values in State are safely handled; (4) tool outcomes, unknown observations, quiesce reasons, control dispositions (UnknownAcceptedEvent.Disposition), and recovery dispositions are typed enums; (5) ToolCallPlannedEvent is idempotent and State tracks LastSequence. |
| Q-13 | 2026-11-03 | F2.4, F2.5 | F2.4 promises payload-inclusive atomicity and triggers (roadmap; storage-security §§3, 6), but ADR 0001 schedules exec_payloads, exec_session_keys and exec_purges in F2.5, which depends on F2.4. A (recommended): bring the physical support schema/sealed-record persistence into F2.4, keeping crypto/key/vault/purge lifecycle and limits in F2.5. B: retain the schema phases and explicitly defer the full protected atomicity proof to F2.5. Details and implementation plan: [F2.4](plans/f2.4-execution-store.md). Which milestone boundary should be adopted? | A approved by the user, 2026-11-03. Physical payload/key/purge schema and already sealed-record persistence enter F2.4; crypto/key/vault/purge lifecycle, size limits and crypto-shred remain F2.5. ADR 0001 and the plan are aligned; implementation has not started. |

## 8. Log

| Date | Item | Change | Updated by |
| --- | --- | --- | --- |
| 2026-11-01 | — | Initial roadmap for the new repository | Designer |
| 2026-11-01 | F0.2 | ADR 0001 package layout approved with amendments (kernel imports nothing of this module; §5 sketch aligned); Q-06 added | Agent |
| 2026-11-01 | F0.3 | Toolchain, `.golangci.yml`, `Taskfile.yml`, `cmd/fuguegate`, gate self-tests and fixtures. golangci-lint pinned at v2.15.0 (v2.13.2 cannot type-check `reflect` on Go 1.27.2); `exhaustruct` is `exhaustruct_v5` (renamed upstream); `uniq-by-line: false` so no linter hides behind another; gremlins' threshold flag exits 0 at 0% so `fuguegate` reads its summary; Q-07 added | Agent |
| 2026-11-01 | F0.1 | Module `github.com/syurodev/fugue`, Apache-2.0 LICENSE, README; Q-01 answered | Agent |
| 2026-11-01 | F0.6 | Added `commit-lint` job and `task hooks` to the item (no `Co-Authored-By`, Conventional Commit PR titles; the item ID is optional); AGENTS.md rule and PR templates added | Agent |
| 2026-11-01 | F0.1–F0.3 | Marked Done after the merge of PRs #1, #2 and #4 | Agent |
| 2026-11-01 | F0.4 | `tools/fuguevet` with `kernelpure`, `nobarego`, `secretlog`, `todoref`, `noprotoleak` and the `vet` step. `kernelpure` also limits `fmt` to formatting functions and `time` to types, constants and methods; `noprotoleak` also covers the protobuf and Connect runtimes (ADR 0001 §6); Q-08 added | Agent |
| 2026-11-01 | F0.5 | `internal/arch` with the nine tests of quality-gates §5 and the ratchets of §8 (`quality/ratchet.json`), each proven on a mini-repository; `docs/error-codes.md` added; Q-09 added. Package docs now must state `Owner:` and the allowed callers, so the docs of `cmd/fuguegate` and `tools/fuguevet` were completed. The ratchet baseline counts five `nolint:gosec` directives (the F0.4 PR text said one) | Agent |
| 2026-11-01 | F0.7 | `proto/fugue/` skeleton, `buf` lint/format/breaking, Go and TypeScript generation, `proto` step in `task check`, `task generate`. The breaking check compares with `origin/main` and says so when main has no proto yet. `TestFileSize` now exempts generated files | Agent |
| 2026-11-01 | F0.8 | `clock` (real and fake), `ids` (UUIDv7, deterministic sources), `errcode` (typed errors, registry, `Expose`), `redact` (`Secret`, masking slog handlers), `supervise` (`Go`, `Task`), `config` (`SYNX_FUGUE_DATA_DIR`, `SYNX_FUGUE_LOG_LEVEL`). `docs/error-codes.md` lists the nine base codes. Two gate-config fixes recorded in quality-gates §11 | Agent |
| 2026-11-01 | F0.4, F0.5, F0.7, F0.8 | Marked Done after the merge of PRs #5 to #8 | Agent |
| 2026-11-01 | F0.6 | CI: `pr` and `nightly` workflows with pinned actions, `actionlint`, `CODEOWNERS`, `fuguegate commitlint`/`commitmsg`/`gateguard`, `task hooks`, and `docs/github-setup.md` for the owner's settings. Q-10 added | Agent |
| 2026-11-01 | F0.6 | CI fixes found on the first run: tool paths read from stdout only, `--allow-parallel-runners` for the linter tests, `buf breaking` against `ref=origin/main`. Caching: a `cache` workflow warms `main`, and the `coverage` job reuses the test profile | Agent |
| 2026-11-01 | F0.6 | PR #9 merged; the `cache` workflow warmed `main` (1 GB caches on Linux and macOS). Status stays In progress until the owner enables the ruleset and the trial PRs are run | Agent |
| 2026-11-01 | F0.6 | Owner enabled the `main` ruleset and squash-only merges; 12 trial PRs proved the gates block (see the F0.6 row). F0.6 Done; F0 complete except the open questions in §7 | Agent |
| 2026-11-01 | F1.1 | `internal/crypto` ported from dev-kit `b3c8ff5d`: same wire format, typed `errcode` errors, injectable random source for vector tests; vectors built from the original code; two fuzz targets; `golang.org/x/crypto` becomes a direct dependency | Agent |
| 2026-11-01 | F1.1 | Marked Done after the merge of PR #24 | Agent |
| 2026-11-01 | F1.2 | `internal/vault`: Master Password setup/unlock/recovery, locked on every start, `SealRecord`/`OpenRecord`, observation API (snapshot, revision, subscription, lock guard) written from `storage-security §8`. The SQL keyring store waits for F1.3 | Agent |
| 2026-11-02 | F1.2 | Marked Done after the merge of PR #26 | Agent |
| 2026-11-02 | F1.5 | `internal/interaction`: in-memory requests per incarnation keyed by operation ID, answer-once confirmation and selection decisions, session grants, one-use execution and selection receipts, and synchronous vault-lock invalidation (`tool-system §5`, D-16) | Agent |
| 2026-11-02 | F1.5 | Marked Done after the merge of PR #27 | Agent |
| 2026-11-02 | F1.7 | `internal/snapshot`: ported shadow-git checkpoint engine and diff parsers from dev-kit `b3c8ff5d`, added separate turn (200/session, 30d) and circular tool (`before`/`after`, 500/session, 7d) quotas, tool-first pruning under the 1 GiB project cap, opaque snapshot ID store, and tool-diff-scoped `PlanToolRestore`/`RestoreTool` (`tool-system §9`) | Agent |
| 2026-11-02 | F1.7 | Marked Done after the merge of PR #28 | Agent |
| 2026-11-02 | F1.3 | `internal/storage` and `vault.SQLKeyringStore`: Q-03 answered (`fugue.db`); SQLite open with `journal_mode=DELETE`, `synchronous=FULL` and `0600` pragma/mode checks, embedded SQL migrations (`0001_initial.sql`), `fugue.lock` with symlink canonicalization and network-FS rejection, `exec_host` incarnation tracking, and `vault_meta` keyring store (`storage-security §2–3, §9`) | Agent |
| 2026-11-02 | F1.3 | Marked Done after the merge of PR #29 | Agent |
| 2026-11-02 | F1.6 | `internal/sandbox`: ported policy validation, Seatbelt SBPL profile generator, `bwrap` argument builder, Landlock `RestrictSelf` and `Runner`/`Proc` process lifecycle from dev-kit `b3c8ff5d` with golden vector tests (`tool-system §6`) | Agent |
| 2026-11-02 | F1.4 | `internal/credentials`: `Book` over `credential_accounts` (secrets sealed by the vault, ChatGPT tokens refreshed under a per-account lock), `PickAccount` ported with the M-09 rule that a ChatGPT plan never fails over. Parsing rate-limit headers into windows is left to the adapters (F4.2, F4.3) | Agent |
| 2026-11-02 | F1.4, F1.6 | Marked Done after the merge of PRs #30 and #31; F1 is complete | Agent |
| 2026-11-02 | F2.1a | Kernel: 54 event types, 27 minimal commands, State, total pure evolve, derived projections, rejection types/codes (Q-12 answered; F2.1 split into F2.1a and F2.1b) | Agent |
| 2026-11-02 | F2.1a, F2.1b | F2.1a marked Done after PR #33 merged with green CI; F2.1b started | Agent |
| 2026-11-02 | F2.1b | Added pure Decide for Trace A with ownership/executor fences, serial tool frontier, atomic response/tool/step batches, identical-observation no-ops and conflict rejection; tests cover Trace A, adversarial evidence, input immutability and deterministic replay | Agent |
| 2026-11-02 | F2.1b | PR #34 opened for review; kernel coverage 98.2%, mutation 96.8%; all-repository race/shuffle tests, Linux lint/deadcode, vet, module, proto and short fuzz gates pass. Native macOS lint/deadcode limitations reproduced on unchanged main 7635107 | Agent |
| 2026-11-02 | F2.1, F2.2 | F2.1/F2.1b marked Done after PR #34 merged with green CI. Q-06 approved for rapid test use; v1.3.0 provenance and full MPL-2.0 text recorded; F2.2 implementation plan added | Agent |
| 2026-11-02 | F2.2 | Added snapshot, transition-batch and history checkers; retained approval, dispatch owner, accounting-window and unknown-exposure evidence; rapid v1.3.0 test-only registration. User approved delivering the roughly 1,600-line implementation and tests in one F2.2 PR instead of splitting the item | Agent |
| 2026-11-02 | F2.2 | PR #37 opened; kernel coverage 95.6%, local mutation 94.9%; Linux lint/deadcode, race/shuffle tests, architecture, module, test-import vulnerability scan, proto and short fuzz pass. Owner applied gate-change-approved for the dependency registry; PR #35 merged and integrated; full native macOS task check passes | Agent |
| 2026-11-03 | F2.2 | Marked Done after PR #37 merged | Designer |
| 2026-11-03 | Q-02, Q-04, Q-05 | Stage A defined by `activation` v0.13: F4.3 becomes ChatGPT-first and the only real adapter; F4.2, F4.4, F7.2 and code reading move to stage B; F7.1 becomes the CLI/TUI client (H-45); F8.1 unblocked; G-06 and G-07 rewritten | Designer |
| 2026-11-03 | F6.3, F5.3 | H-24 settled in `interaction`: F6.3 gets its design and evidence; F5.3 adds the interaction service contract and depends on F6.3 | Designer |
| 2026-11-03 | F6.4, F6.5, F6.6 | H-25 and H-18 settled in `sandbox-runtime` and `shell-job`: F6.4 gets its design; shell and job split from F6.5 into F6.6 | Designer |
| 2026-11-03 | F6.6 | `shell-job` adds compact/full output, built-in output filters, `shell.output` replacing `job.output`, and per-program grammar for grant scope; evidence moves to `shell-job §8` | Designer |
| 2026-11-03 | F6.5, F6.6, F6.7, F6.8 | H-17, H-26 and H-27 settled in `file-tools` and `content-safety`: file tools split from F6.5 into F6.8, content safety becomes F6.7, and F6.6 depends on F6.7 for redaction | Designer |
| 2026-11-03 | F2.3 | Completed R9 field dominance: named JSON tags win over untagged candidates at equal depth, including recovery from untagged ambiguity; added TestCanonicalFieldTagDominance against encoding/json. Local task check passes (codec coverage 92.2%, mutation 89.8%). PR #38 remains In review | Agent |
| 2026-11-03 | F2.3 | Integrated main roadmap decisions; aligned UTF-8 traversal with JSON container depth and selected fields; removed the embedding-depth cap with cycle-safe field discovery; added public round-trip and rejection evidence. Local task check passes (codec coverage 92.7%, mutation 88.2%). PR #38 remains In review | Agent |
| 2026-11-03 | F2.3, F2.4 | Marked F2.3 Done after PR #38 merged; drafted the F2.4 implementation/evidence plan and recorded Q-13 as the only confirmed phasing conflict. F2.4 Needs decision; no design/ADR/gate changes applied | Agent |
| 2026-11-03 | Q-13, F2.4, F2.5 | User selected A. Aligned the three support-table phases in ADR 0001 and the execution-store plan; F2.4 returns to Not started. Cryptographic lifecycle and purge behavior remain F2.5; no implementation or migration added | Agent |
| 2026-11-04 | F6.5, F6.9, F7.1 | H-20 and H-46 settled in `ask-plan` and `tasks`: F6.5 gets its design, tasks and checklists become F6.9, and F7.1 adds `fugue tasks` | Designer |
| 2026-11-04 | F2.4 | `internal/execstore` and migration `0003_execution_store.sql`: execution-store schema, append-only triggers, single-transaction atomic batch commit across events, payloads, projections, receipts and admission fences, crash rollback before commit, uncertain commit read-back, receipt deduplication, and deterministic event replay. PR #42 opened for review; execstore coverage 90.8%, mutation 89.9%; all PR gates green | Agent |
| 2026-11-04 | F2.4 | Resolved review findings R1–R8: incremental state folding across transactions, context cancellation transaction rollback with clean connection pool verification, queue fence admission boundary enforcement, metadata-only plaintext allowlist, immediate rollback on found receipt to prevent lock leaks, canonical transcript input materialization on RunAdmitted, strict session binding on protected equality deduplication, and full read-back pragma/autocommit recovery verification. Added regression tests TestRegressionR1–TestRegressionR8; PR #42 remains In review | Agent |
| 2026-11-04 | F2.4 | Resolved follow-up review findings A–F: typed metadata schemas with strict codec unmarshaling and safe diagnostics (A), connection lease release prior to protected deduplication equality verifications (B), canonical transcript input reference resolution from accepted facts (C), queue fence execution event and token authority boundaries (D), durable pragma configuration and verification on every physical connection acquired (E), and stable retry canonicalization upfront before deduplication preflight (F). Added regression tests TestRegressionFollowupA–TestRegressionFollowupF; execstore coverage 90.1%, mutation 89.5%; PR #42 remains In review | Agent |
| 2026-11-04 | F2.4 | Closed remaining authority/transcript review gaps: first-session admission validates host/event fences and positive tokens, queued writes use command/event allowlists with zero admission tokens and cannot target admitted history, and input is projected only on RunAdmitted even in combined accept/admit batches. Added authority regression tests; PR #42 remains In review. Earlier CI run 41776951230 applies to ee669df only, not the A–F follow-up commits | Agent |
| 2026-11-04 | F2.7 | Added the minimal shared request/stream contract and deterministic faux provider with classified failures, cut points, cancellation and call counters; local `task check` passes (race/shuffle, architecture, coverage, fuzz-short included); mutation-changed selected 0 guarded packages | Agent |
| 2026-11-04 | F2.7 | Opened [PR #44](https://github.com/syurodev/fugue/pull/44) from `feat/f2.7-faux-provider`; awaiting CI and review, not marked Done | Agent |
| 2026-11-04 | F2.7 | Addressed PR #44 review finding by cloning `Failure` pointers on script retention and event emission; added ownership regression tests; local `task check` passes | Agent |
| 2026-11-04 | F5.6, F6.4, F6.8, F7.1 | H-34 settled in `workspace`: workspaces and configuration become F5.6, F6.4 and F6.8 depend on it for roots and mounts, and F7.1 adds `fugue workspace` | Designer |
