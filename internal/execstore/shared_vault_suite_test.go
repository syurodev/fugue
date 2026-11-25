package execstore

import (
	"testing"

	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
)

func TestSharedVaultSuite(t *testing.T) {
	t.Parallel()
	v := cheapvault.Unlocked(t)
	cases := []struct {
		name string
		fn   func(*testing.T, *vault.Service)
	}{
		{"PayloadUsageValidation", runPayloadUsageValidation},
		{"PayloadUsageReporting", runPayloadUsageReporting},
		{"ProtectedInputAdditionEmptyAndInBatch", runProtectedInputAdditionEmptyAndInBatch},
		{"ProtectedInputAdditionCalculation", runProtectedInputAdditionCalculation},
		{"ProtectedInputAdditionNewRun", runProtectedInputAdditionNewRun},
		{"RunAndSessionOverflowLimits", runRunAndSessionOverflowLimits},
		{"SessionKeyReusedAcrossMultiplePayloads", runSessionKeyReusedAcrossMultiplePayloads},
		{"ConcurrentFirstWritesAndRollbackNoKeyMismatch", runConcurrentFirstWritesAndRollbackNoKeyMismatch},
		{"TamperingAndSubstitutionRejections", runTamperingAndSubstitutionRejections},
		{"PayloadSizeLimitsAndBoundaries", runPayloadSizeLimitsAndBoundaries},
		{"NoSensitiveDataInDiagnostics", runNoSensitiveDataInDiagnostics},
		{"StoreVaultGetter", runStoreVaultGetter},
		{"SessionKeyConflictAndMatching", runSessionKeyConflictAndMatching},
		{"PayloadValidationEdgeCases", runPayloadValidationEdgeCases},
		{"PayloadOpenValidationEdgeCases", runPayloadOpenValidationEdgeCases},
		{"KeyCacheOperations", runKeyCacheOperations},
		{"OpenPayloadCorruptedEnvelope", runOpenPayloadCorruptedEnvelope},
		{"PurgeSessionCryptoShredAndDirectDBByteInspection", runPurgeSessionCryptoShredAndDirectDBByteInspection},
		{"PurgeSessionIsolationAndSafeRetry", runPurgeSessionIsolationAndSafeRetry},
		{"PurgeSessionPreventsResurrection", runPurgeSessionPreventsResurrection},
		{"PurgeSessionRollbackPreservesSession", runPurgeSessionRollbackPreservesSession},
		{"PurgeValidationEdgeCases", runPurgeValidationEdgeCases},
		{"SealPayloadFailsClosedOnPreparationErrors", runSealPayloadFailsClosedOnPreparationErrors},
		{"SealPayloadRejectsNonceEntropyFailure", runSealPayloadRejectsNonceEntropyFailure},
		{"RegressionPayloadSizeCannotBeUnderstated", runRegressionPayloadSizeCannotBeUnderstated},
		{"RegressionFirstKeyRollbackCanPrepareAgain", runRegressionFirstKeyRollbackCanPrepareAgain},
		{"RegressionFailedPurgeKeepsSessionUsable", runRegressionFailedPurgeKeepsSessionUsable},
		{"SessionKeyRandomFailureIsZeroedAndRetryable", runSessionKeyRandomFailureIsZeroedAndRetryable},
		{"PurgeDeleteFailureRestoresAccess", runPurgeDeleteFailureRestoresAccess},
		{"PayloadAccountingRejectsMalformedEnvelope", runPayloadAccountingRejectsMalformedEnvelope},
		{"CanceledPurgeDoesNotPoisonSession", runCanceledPurgeDoesNotPoisonSession},
		{"ConflictingKeyCandidateCanReprepare", runConflictingKeyCandidateCanReprepare},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, v)
		})
	}
}
