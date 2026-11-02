# Ports

Provenance of every package ported from `syurodev/dev-kit` (roadmap §3). One row per package,
one PR per package. The vector tests of a port are built from the original code's output; they
must not be regenerated from the ported code.

Source repository state: `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` (`feat(harness): enforce
exact and metadata closure contracts (#419)`).

## `internal/crypto` (F1.1)

| | |
| --- | --- |
| Original | `internal/crypto` (`aead.go`, `envelope.go`, `kdf.go`, `keyring.go` and their tests) |
| dev-kit commit | `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` |
| Wire and storage format | Unchanged: envelope v2 JSON, canonical associated data `type NUL id NUL key`, XChaCha20-Poly1305, Argon2id with the same bounds and default cost, keyring v1 JSON |
| Vector tests | `vectors_test.go` and `testdata/keyring.json`: envelopes, derived keys and a whole keyring produced by the original code from a fixed random stream and a fixed key. `TestSealMatchesTheOriginalVector`, `TestSealOfEmptyPlaintextMatchesTheOriginalVector`, `TestDeriveKeyMatchesTheOriginalVectors`, `TestNewKeyringMatchesTheOriginalVector`, `TestTheOriginalKeyringStillUnlocks` |

Changed, without changing what is accepted or produced:

- Errors are `errcode` errors with stable codes instead of ad hoc strings. A wrong password, a
  wrong recovery key, a wrong AEAD key, a changed record identity and a tampered envelope are
  `permission_denied`; malformed envelopes, associated data or KDF parameters are
  `invalid_argument`; a failing random source is `internal`.
- Randomness is read from an `io.Reader` parameter of unexported twins (`sealV2`,
  `newKDFParams`, `newKeyring`, `changePassword`); the exported functions pass `crypto/rand`.
  The original read `crypto/rand` directly, so its output could not be fixed in a test.
- `SealV2` returns a zero `Envelope` with the error when the ciphertext would exceed
  `MaxCiphertextBytes`; the original returned the envelope next to the error.
- Redundant checks were merged: the repeated version and algorithm checks in `OpenWithAD`,
  `open` and `Validate` are one `Validate`, and the empty-salt check is covered by the salt
  length bound. The same inputs are accepted and rejected.
- `OpenWithAD` and `Envelope.Validate` take a pointer (`gocritic` `hugeParam`: an `Envelope` is 88 bytes); a nil envelope is rejected as `invalid_argument`.
- `AlgorithmAES256GCM` is not carried over: the original defined it only to reject it.
- DevKit wording ("sync wire limit") is removed from comments.

## `internal/vault` (F1.2)

| | |
| --- | --- |
| Original | `internal/vault` (`service.go`, `record_codec.go`, `keyringstore.go`, `sqlkeyringstore.go` and their tests) |
| dev-kit commit | `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` |
| Stored format | Unchanged: the keyring JSON and the record envelope JSON |
| Vector tests | `testdata/keyring-cheap.json` and `testdata/record-golden.json` were produced by the original crypto code; `TestARecordSealedByTheOriginalCodeStillOpens`, `TestUnlockOpensTheKeyringsDataKey` |

The lock lifecycle (`Setup`, `Unlock`, `UnlockWithRecovery`, `Lock`), the Master Password length rule
(8 bytes), the record bounds (1,000,000 bytes of plaintext, 1,500,000 of envelope) and the record
binding follow the original. The observation API is not a copy: it is written from
`storage-security §8`.

Changed:

- Secrets cross the API as `redact.Secret`, and the store port takes a `context.Context`.
- The incarnation comes from the injected `ids.Generator`, not `crypto/rand`.
- Errors are `errcode` errors. `ErrLocked` is `failed_precondition`; a wrong password or recovery key is
  `permission_denied`; a store failure is `internal`.
- `Unlock` derives the key under the lifecycle mutex only, so sealing is not blocked during the slow
  derivation. `Lock` changes the state before it runs the guards and zeroes the key afterwards, so no
  operation succeeds once the guards run and the old `locking` flag is gone.
- Subscriptions keep their flags under the vault mutex instead of atomics, and the drain loop of
  `Resync` cannot block.

Not carried over:

- `FileKeyringStore`: the keyring lives in `vault_meta` (ADR 0001 §5). `SQLKeyringStore` follows in F1.3,
  when storage can create the table; `MemoryKeyringStore` serves tests and ephemeral runs.
- `VerifyMasterPassword`, `Status`, `IsLocked` and the exported `SealV2`/`OpenWithAD` methods: no
  roadmap item needs them. Credentials may need `VerifyMasterPassword` in F1.4.
- The check on the encoded envelope length after sealing: a 1,000,000-byte record always encodes
  below 1,500,000 bytes, so the branch was unreachable.

## `internal/interaction` (F1.5)

| | |
| --- | --- |
| Original | `internal/interaction` (`hub.go`, `errors.go` and their tests) |
| dev-kit commit | `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` |
| Stored format | None: requests, session grants and receipts are held only in memory per incarnation (`tool-system §5.1`, D-16) |
| Vector tests | Behavioral invariants from `hub_test.go` and `scoped_approval_test.go`: `TestAnOldRequestCannotBeAnsweredAfterRestart`, `TestADuplicateAnswerIsRejected`, `TestAReceiptCanBeClaimedOnlyOnceAndMatchesItsBinding`, `TestSelectionRequestsAcceptOnlyOfferedOptionsAndNeverAuthorizeExecution`, `TestImportantRequestsAlwaysAskAndRefuseSessionGrants`, `TestVaultLockInvalidatesPendingRequestsGrantsAndReceipts`, `TestNilResponderAndHeadlessFailClosed` |

The fail-closed rules of `Hub` are preserved: a nil responder, headless mode or a locked vault
never approves a request; environment confirmation and `Important` requests always ask and never
offer or reuse a session grant; selections accept only an option that was offered; and vault lock
is checked inside the owner's critical section when claiming a receipt or answering a request.

Changed for `tool-system §5` and D-16:

- Worker goroutines no longer block in `wait`: `Ensure` creates or reads back a request keyed by
  `OperationID` in the current owner incarnation, and `Answer` resolves it at most once.
- Approvals issue a one-use `Receipt` bound to the session, workspace, invocation, attempt, check
  generation, tool, argument digest and target/environment revisions; `ClaimReceipt` and
  `ClaimSelection` consume it atomically.
- Time and identifiers come from the injected `clock.Clock` and `ids.Generator` ports, and errors
  use `errcode` codes.

Not carried over:

- DevKit-specific prompt helpers (`SelectAccount`, `SelectHarnessScope`,
  `SelectGroupRequestAnswer`, `ConfirmAgent`) and `db.query` multi-use scoped approvals: the full
  per-tool interaction surface is H-24 (F6.3).

## `internal/snapshot` (F1.7)

| | |
| --- | --- |
| Original | `internal/checkpoint` (`snapshot.go`, `repo.go`, `restore.go`, `diff.go`, `maintenance.go`, `locate.go`, `activity.go` and their tests) and `internal/gitparse` (`gitparse.go`, `gitparse_test.go`) |
| dev-kit commit | `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` |
| Stored format | Shadow Git repository per project (`refs/fugue/cp/<id>/<slot>`, `fugue-project` marker, `info/attributes`); snapshot store maps random snapshot IDs to `BeforeTree`/`AfterTree` git tree hashes |
| Vector tests | Behavioral and git-output tests from `checkpoint_test.go`, `restore_test.go`, `maintenance_test.go`, `locate_test.go`, `activity_test.go` and `gitparse_test.go`: `TestDenyListBeatsGitignoreNegation`, `TestRestoreKeepsSymlinksAndExecutableBit`, `TestRestoreIsByteExactUnderProjectGitattributes`, `TestRestoreRefusesToWriteThroughASymlinkedDirectory`, `TestRestoreNeverTouchesASkippedPath`, `TestRestoreReportsAndCanOverrideAConflict`, `TestApplyRefusesAStalePlan`, `TestPlanRestoreNarrowsToTheGivenPaths`, `TestMaintainDropsOrphanRefsAndKeepsLiveTrees`, `TestPorcelainAndRawParsers` |

Changed for `tool-system §9`:

- Two separate quotas (`KindTurn`: 200 per session, 30 days; `KindTool`: 500 per session circular, 7 days) so tool snapshots never evict turn snapshots (`TestToolSnapshotQuotaNeverEvictsTurnSnapshots`), and the shared 1 GiB per-project cap prunes tool snapshots before turn snapshots (`TestSoftCapPrunesToolSnapshotsBeforeTurnSnapshots`).
- `CaptureTurn`, `CaptureToolBefore`, `CaptureToolAfter`, `PlanToolRestore` and `RestoreTool` issue random snapshot IDs via `ids.Generator` while keeping git tree hashes inside `Store`, and tool restore narrows `PlanRestore` to the files in that tool's own `before`/`after` diff (`TestRestoreToolTouchesOnlyTheToolsDiff`).
- `gitparse` is folded into `internal/snapshot` (ADR 0001 §2). Time, random IDs and test goroutines go through `clock.Clock`, `ids.Generator` and `supervise.Go`; `LocateWith` takes `*LocateOptions` instead of package-level globals.


