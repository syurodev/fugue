package app

import (
	"testing"

	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
)

type sharedTranscriptVaultCase struct {
	name string
	fn   func(*testing.T, *vault.Service)
}

func sharedTranscriptVaultCases() []sharedTranscriptVaultCase {
	return []sharedTranscriptVaultCase{
		{"TestActorFeedsQuotaAndFailedAuthToTheVendorAccount", runActorFeedsQuotaAndFailedAuthToTheVendorAccount},
		{"TestTeamSubTasksGetLowerCPUWeight", runTeamSubTasksGetLowerCPUWeight},
		{"TestTranscriptPageRPCDefaultsTo200AndClampsAt500NewestFirst", runTranscriptPageRPCDefaultsTo200AndClampsAt500NewestFirst},
		{"TestTranscriptItemRPCReturnsFullRedactedContent", runTranscriptItemRPCReturnsFullRedactedContent},
		{"TestTranscriptRPCsDenyNonLocalPrincipal", runTranscriptRPCsDenyNonLocalPrincipal},
		{"TestUnknownTranscriptSessionAndItemAreTyped", runUnknownTranscriptSessionAndItemAreTyped},
		{"TestVendorOriginTurnsAreGroupedAndAbsorbedNoticeStaysOnFugueTurn", runVendorOriginTurnsAreGroupedAndAbsorbedNoticeStaysOnFugueTurn},
		{"TestCompleteBeforeAppendSoNoStaleDeltaFollowsTheDurableEvent", runCompleteBeforeAppendSoNoStaleDeltaFollowsTheDurableEvent},
		{"TestPurgedSessionTopicsAreUnregisteredAndDenied", runPurgedSessionTopicsAreUnregisteredAndDenied},
		{"TestRedactionMasksOneMiB", runRedactionMasksOneMiB},
		{"TestFailedStopKeepsPendingOwner", runFailedStopKeepsPendingOwner},
		{"TestActorRetiringDuringPeerCheckStillDenies", runActorRetiringDuringPeerCheckStillDenies},
		{"TestSendAppendFailureClosesSession", runSendAppendFailureClosesSession},
		{"TestUserInterruptKeepsDrainingObservations", runUserInterruptKeepsDrainingObservations},
		{"TestUserInterruptEscalationRecordsUnknown", runUserInterruptEscalationRecordsUnknown},
		{"TestCloseCompletesWhileSendIsWedged", runCloseCompletesWhileSendIsWedged},
		{"TestCloseAllCompletesWhileSendIsWedged", runCloseAllCompletesWhileSendIsWedged},
		{"TestActorPersistsVendorInitiatedTurnFromLateSteer", runActorPersistsVendorInitiatedTurnFromLateSteer},
		{"TestVendorSessionSteerInterruptAndList", runVendorSessionSteerInterruptAndList},
		{"TestActorAbsorbedSendEndsFugueTurn", runActorAbsorbedSendEndsFugueTurn},
		{"TestActorProcessExitPersistsStopProofBeforeClose", runActorProcessExitPersistsStopProofBeforeClose},
		{"TestVendorSessionResumeUsesStoredHandleAndStartsNewProcess", runVendorSessionResumeUsesStoredHandleAndStartsNewProcess},
		{"TestActorAppendFailureDoesNotAdvanceSequence", runActorAppendFailureDoesNotAdvanceSequence},
		{"TestVendorSessionCapacityRejectsThirdActiveProcess", runVendorSessionCapacityRejectsThirdActiveProcess},
		{"TestActorObservationBackpressurePreservesAll", runActorObservationBackpressurePreservesAll},
		{"TestActorUsesCommittedTranscriptPath", runActorUsesCommittedTranscriptPath},
		{"TestCloseRPCDeadlineDoesNotSkipStopOrder", runCloseRPCDeadlineDoesNotSkipStopOrder},
	}
}

// TestSharedTranscriptVaultSuite shares one unlocked cheapvault across
// independent tests that never lock or rotate the vault, avoiding repeated
// Argon2id derivations under -race while keeping each subtest isolated on its
// own SQLite database and event hub.
func TestSharedTranscriptVaultSuite(t *testing.T) {
	t.Parallel()
	v := cheapvault.Unlocked(t)
	for _, tc := range sharedTranscriptVaultCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, v)
		})
	}
}
