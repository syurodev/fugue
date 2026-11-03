package codec

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	receiptAdmitRun     = "AdmitRun"
	receiptAcceptRun    = "AcceptRun"
	receiptRequestStop  = "RequestStop"
	receiptResumeRun    = "ResumeRun"
	receiptAlice        = "user-alice"
	receiptSessionAlpha = "session-alpha"
	receiptSessionOne   = "sess-1"
	receiptAdmin        = "admin"
	receiptRun42        = "run-42"
)

func sampleBaseReceipt() Receipt {
	return Receipt{
		CommandID:      "cmd-200",
		Kind:           receiptAcceptRun,
		Principal:      receiptAlice,
		Target:         receiptSessionAlpha,
		CodecVersion:   CodecV1,
		InputPayloadID: "pay-1",
		MetadataOnly:   false,
		CanonicalInput: nil,
		Result: ReceiptResult{
			FirstSequence: 1,
			LastSequence:  1,
			RejectionCode: "",
			Message:       "ok",
		},
	}
}

func TestReceiptDeduplicationSameCommandID(t *testing.T) {
	t.Parallel()

	input := []byte(`{"RunID":"run-1","Origin":"user-main"}`)
	existing := Receipt{
		CommandID:      "cmd-100",
		Kind:           receiptAdmitRun,
		Principal:      "admin-user",
		Target:         receiptSessionOne,
		CodecVersion:   CodecV1,
		InputPayloadID: "payload-99",
		MetadataOnly:   false,
		CanonicalInput: nil,
		Result: ReceiptResult{
			FirstSequence: 1,
			LastSequence:  2,
			RejectionCode: "",
			Message:       "committed",
		},
	}

	newInput := make([]byte, len(input))
	copy(newInput, input)

	req := DedupRequest{
		Existing:               existing,
		ExistingInput:          input,
		ExistingInputAvailable: true,
		NewKind:                receiptAdmitRun,
		NewPrincipal:           "admin-user",
		NewTarget:              receiptSessionOne,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      newInput,
	}

	decision, err := CheckDedup(&req)
	if err != nil {
		t.Fatalf("unexpected error for identical command: %v", err)
	}
	if decision.Status != DedupStatusMatch {
		t.Fatalf("expected DedupStatusMatch, got %s", decision.Status)
	}
	if diff := cmp.Diff(existing, decision.Receipt); diff != "" {
		t.Fatalf("receipt mismatch (-want +got):\n%s", diff)
	}
}

func TestReceiptDedupDifferentPayload(t *testing.T) {
	t.Parallel()

	origInput := []byte(`{"Prompt":"Write code"}`)
	diffInput := []byte(`{"Prompt":"Delete database"}`)
	baseReceipt := sampleBaseReceipt()

	req := DedupRequest{
		Existing:               baseReceipt,
		ExistingInput:          origInput,
		ExistingInputAvailable: true,
		NewKind:                receiptAcceptRun,
		NewPrincipal:           receiptAlice,
		NewTarget:              receiptSessionAlpha,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      diffInput,
	}

	decision, err := CheckDedup(&req)
	if !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected ErrReceiptConflict, got %v", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict, got %s", decision.Status)
	}
}

func TestReceiptDedupDifferentKind(t *testing.T) {
	t.Parallel()

	origInput := []byte(`{"Prompt":"Write code"}`)
	baseReceipt := sampleBaseReceipt()

	req := DedupRequest{
		Existing:               baseReceipt,
		ExistingInput:          origInput,
		ExistingInputAvailable: true,
		NewKind:                receiptRequestStop,
		NewPrincipal:           receiptAlice,
		NewTarget:              receiptSessionAlpha,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      origInput,
	}

	decision, err := CheckDedup(&req)
	if !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected ErrReceiptConflict, got %v", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict, got %s", decision.Status)
	}
}

func TestReceiptDedupDifferentPrincipal(t *testing.T) {
	t.Parallel()

	origInput := []byte(`{"Prompt":"Write code"}`)
	baseReceipt := sampleBaseReceipt()

	req := DedupRequest{
		Existing:               baseReceipt,
		ExistingInput:          origInput,
		ExistingInputAvailable: true,
		NewKind:                receiptAcceptRun,
		NewPrincipal:           "attacker-bob",
		NewTarget:              receiptSessionAlpha,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      origInput,
	}

	decision, err := CheckDedup(&req)
	if !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected ErrReceiptConflict, got %v", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict, got %s", decision.Status)
	}
}

func TestReceiptDedupDifferentTarget(t *testing.T) {
	t.Parallel()

	origInput := []byte(`{"Prompt":"Write code"}`)
	baseReceipt := sampleBaseReceipt()

	req := DedupRequest{
		Existing:               baseReceipt,
		ExistingInput:          origInput,
		ExistingInputAvailable: true,
		NewKind:                receiptAcceptRun,
		NewPrincipal:           receiptAlice,
		NewTarget:              "session-beta",
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      origInput,
	}

	decision, err := CheckDedup(&req)
	if !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected ErrReceiptConflict, got %v", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict, got %s", decision.Status)
	}
}

func TestCodecVersionMismatch(t *testing.T) {
	t.Parallel()

	input := []byte(`{"RunID":"run-1"}`)
	receipt := Receipt{
		CommandID:      "cmd-v1",
		Kind:           receiptResumeRun,
		Principal:      "p1",
		Target:         "t1",
		CodecVersion:   CodecV1,
		InputPayloadID: "pay-v1",
		MetadataOnly:   false,
		CanonicalInput: nil,
		Result:         ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	}

	req := DedupRequest{
		Existing:               receipt,
		ExistingInput:          input,
		ExistingInputAvailable: true,
		NewKind:                receiptResumeRun,
		NewPrincipal:           "p1",
		NewTarget:              "t1",
		NewCodecVersion:        "v2",
		NewCanonicalInput:      input,
	}

	decision, err := CheckDedup(&req)
	if err == nil {
		t.Fatalf("expected ErrCodecVersionMismatch, got nil")
	}
	if !errors.Is(err, ErrCodecVersionMismatch) {
		t.Fatalf("expected ErrCodecVersionMismatch, got %v", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict, got %s", decision.Status)
	}
}

func sampleMetadataReceipt(input []byte) Receipt {
	return Receipt{
		CommandID:      "cmd-meta-1",
		Kind:           receiptRequestStop,
		Principal:      receiptAdmin,
		Target:         receiptRun42,
		CodecVersion:   CodecV1,
		InputPayloadID: "",
		MetadataOnly:   true,
		CanonicalInput: input,
		Result: ReceiptResult{
			FirstSequence: 10,
			LastSequence:  10,
			RejectionCode: "",
			Message:       "stopped",
		},
	}
}

func TestReceiptDedupMetadataMatch(t *testing.T) {
	t.Parallel()

	input := []byte(`{"Reason":"user_cancel","Mode":"terminal"}`)
	receipt := sampleMetadataReceipt(input)

	req := DedupRequest{
		Existing:               receipt,
		ExistingInput:          nil,
		ExistingInputAvailable: false,
		NewKind:                receiptRequestStop,
		NewPrincipal:           receiptAdmin,
		NewTarget:              receiptRun42,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      input,
	}
	decision, err := CheckDedup(&req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Status != DedupStatusMatch {
		t.Fatalf("expected DedupStatusMatch, got %s", decision.Status)
	}
	if diff := cmp.Diff(receipt, decision.Receipt); diff != "" {
		t.Fatalf("receipt mismatch (-want +got):\n%s", diff)
	}
}

func TestReceiptDedupMetadataConflict(t *testing.T) {
	t.Parallel()

	input := []byte(`{"Reason":"user_cancel","Mode":"terminal"}`)
	otherInput := []byte(`{"Reason":"timeout","Mode":"graceful"}`)
	receipt := sampleMetadataReceipt(input)

	req := DedupRequest{
		Existing:               receipt,
		ExistingInput:          nil,
		ExistingInputAvailable: false,
		NewKind:                receiptRequestStop,
		NewPrincipal:           receiptAdmin,
		NewTarget:              receiptRun42,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      otherInput,
	}
	decision, err := CheckDedup(&req)
	if !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected ErrReceiptConflict, got %v", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict, got %s", decision.Status)
	}
}

func TestReceiptDeduplicationVaultLockedFailClosed(t *testing.T) {
	t.Parallel()

	input := []byte(`{"SecretInstruction":"deploy"}`)
	receipt := Receipt{
		CommandID:      "cmd-secure-1",
		Kind:           receiptAcceptRun,
		Principal:      "owner",
		Target:         "session-vault",
		CodecVersion:   CodecV1,
		InputPayloadID: "payload-sealed-1",
		MetadataOnly:   false,
		CanonicalInput: nil,
		Result: ReceiptResult{
			FirstSequence: 1,
			LastSequence:  1,
			RejectionCode: "",
			Message:       "",
		},
	}

	req := DedupRequest{
		Existing:               receipt,
		ExistingInput:          nil,
		ExistingInputAvailable: false,
		NewKind:                receiptAcceptRun,
		NewPrincipal:           "owner",
		NewTarget:              "session-vault",
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      input,
	}

	decision, err := CheckDedup(&req)
	if !errors.Is(err, ErrVaultRequired) {
		t.Fatalf("expected ErrVaultRequired when vault is locked, got %v", err)
	}
	if decision.Status != DedupStatusVaultRequired {
		t.Fatalf("expected DedupStatusVaultRequired, got %s", decision.Status)
	}
}

func TestReceiptResultHelpers(t *testing.T) {
	t.Parallel()

	okResult := ReceiptResult{
		FirstSequence: 1,
		LastSequence:  5,
		RejectionCode: "",
		Message:       "success",
	}
	if okResult.IsRejection() {
		t.Fatalf("okResult should not be rejection")
	}

	rejResult := ReceiptResult{
		FirstSequence: 0,
		LastSequence:  0,
		RejectionCode: "invalid_state",
		Message:       "cannot proceed",
	}
	if !rejResult.IsRejection() {
		t.Fatalf("rejResult should be rejection")
	}
}

func TestReceiptNilRequest(t *testing.T) {
	t.Parallel()

	decision, err := CheckDedup(nil)
	if !errors.Is(err, ErrNilTarget) {
		t.Fatalf("CheckDedup(nil) = %v, want ErrNilTarget", err)
	}
	if decision.Status != DedupStatusConflict {
		t.Fatalf("expected DedupStatusConflict for nil request, got %s", decision.Status)
	}
}

func TestReceiptDiagnosticsNoSecrets(t *testing.T) {
	t.Parallel()

	secretPayload := "SUPER_SECRET_TOKEN_DO_NOT_LEAK"
	inputA := []byte(`{"Token":"` + secretPayload + `"}`)
	inputB := []byte(`{"Token":"DIFFERENT_SECRET"}`)

	receipt := Receipt{
		CommandID:      "cmd-leak-check",
		Kind:           receiptAcceptRun,
		Principal:      receiptAdmin,
		Target:         receiptSessionOne,
		CodecVersion:   CodecV1,
		InputPayloadID: "pay-1",
		MetadataOnly:   false,
		CanonicalInput: nil,
		Result:         ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	}

	req := DedupRequest{
		Existing:               receipt,
		ExistingInput:          inputA,
		ExistingInputAvailable: true,
		NewKind:                receiptAcceptRun,
		NewPrincipal:           receiptAdmin,
		NewTarget:              receiptSessionOne,
		NewCodecVersion:        CodecV1,
		NewCanonicalInput:      inputB,
	}

	_, err := CheckDedup(&req)
	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}
	errMsg := err.Error()
	for _, secret := range []string{secretPayload, "DIFFERENT_SECRET"} {
		if strings.Contains(errMsg, secret) {
			t.Fatalf("error message leaked secret payload: %s", errMsg)
		}
	}
}
