package codec

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/kernel"
)

const (
	testRunID        = "run-1"
	testOrigin       = "origin-1"
	testSessionID    = "sess-1"
	testInvocationID = "inv-1"
	testAttemptID    = "att-1"
	testToolCallID   = "tc-1"
	testAdmin        = "admin"
	testUser         = "user-1"
)

func sampleRunLifecycleCommands() []kernel.Command {
	return []kernel.Command{
		&kernel.AcceptRunCommand{
			CommandID:       "cmd-1",
			RunID:           testRunID,
			Origin:          testOrigin,
			InputRef:        "ref-in",
			SessionID:       testSessionID,
			QueueItemRef:    "q-1",
			ProtocolBinding: "proto-1",
			InitialBudgets:  "budget-1",
			PriorityClass:   "interactive",
			PeerOutboxRef:   "peer-1",
			GoalID:          "goal-1",
			GoalRound:       1,
		},
		&kernel.AdmitRunCommand{
			CommandID:              "cmd-2",
			RunID:                  testRunID,
			Origin:                 testOrigin,
			Token:                  10,
			OwnerIncarnation:       1,
			QueueDecisionRef:       "dec-1",
			ContextEpoch:           1,
			StartRunID:             testRunID,
			InitialContextRevision: 1,
		},
		&kernel.AcquireRunOwnershipCommand{
			CommandID:                   "cmd-3",
			RunID:                       testRunID,
			Origin:                      testOrigin,
			NewToken:                    11,
			PreviousToken:               10,
			RecoveryReceipt:             "rcpt-1",
			DataDirectoryLockGeneration: 2,
		},
		&kernel.AdvanceRunCommand{
			CommandID: "cmd-4",
			RunID:     testRunID,
			Origin:    testOrigin,
			Token:     11,
			StepIndex: 1,
		},
	}
}

func sampleRunDispatchCommands() []kernel.Command {
	return []kernel.Command{
		&kernel.PrepareDispatchCommand{
			CommandID:    "cmd-5",
			RunID:        testRunID,
			Origin:       testOrigin,
			Token:        11,
			InvocationID: testInvocationID,
			AttemptID:    testAttemptID,
		},
		&kernel.RecoverRunCommand{
			CommandID:       "cmd-17",
			RunID:           testRunID,
			Origin:          testOrigin,
			Token:           11,
			InterruptionRef: "int-1",
			Evidence:        "recovered",
		},
		&kernel.ResumeRunCommand{
			CommandID: "cmd-22",
			RunID:     testRunID,
			Origin:    testOrigin,
			Token:     12,
			Actor:     testUser,
			Evidence:  "resumed",
		},
	}
}

func sampleModelCommands() []kernel.Command {
	return []kernel.Command{
		&kernel.ObserveModelStreamCommand{
			CommandID:     "cmd-6",
			RunID:         testRunID,
			Origin:        testOrigin,
			Token:         11,
			AttemptID:     testAttemptID,
			InvocationID:  testInvocationID,
			FirstChunkRef: "chunk-1",
		},
		&kernel.ObserveModelResponseCommand{
			CommandID:    "cmd-7",
			RunID:        testRunID,
			Origin:       testOrigin,
			Token:        11,
			AttemptID:    testAttemptID,
			InvocationID: testInvocationID,
			ResponseRef:  "resp-1",
			UsageStatus:  "ok",
		},
		&kernel.ObserveModelFailureCommand{
			CommandID:        "cmd-8",
			RunID:            testRunID,
			Origin:           testOrigin,
			Token:            11,
			AttemptID:        testAttemptID,
			InvocationID:     testInvocationID,
			Stage:            "dispatch",
			FailureClass:     "provider_error",
			FailureCode:      "rate_limit",
			RetryDisposition: "retryable",
			UsageStatus:      "metered",
			PartialRef:       "part-1",
		},
		&kernel.ObserveAccountingCommand{
			CommandID:    "cmd-23",
			RunID:        testRunID,
			Origin:       testOrigin,
			AttemptID:    testAttemptID,
			WindowID:     "win-1",
			TokensUsed:   500,
			DurationUsed: 5 * time.Second,
			Basis:        "exact",
		},
		&kernel.ExtendBudgetCommand{
			CommandID:       "cmd-24",
			RunID:           testRunID,
			Origin:          testOrigin,
			Kind:            "step_limit",
			ExtensionAmount: 10,
			Actor:           testUser,
		},
	}
}

func sampleToolCommands() []kernel.Command {
	return []kernel.Command{
		&kernel.ObserveToolCheckCommand{
			CommandID:          "cmd-9",
			RunID:              testRunID,
			Origin:             testOrigin,
			Token:              11,
			ToolCallID:         testToolCallID,
			AttemptID:          testAttemptID,
			CheckGeneration:    1,
			ValidatedArgsRef:   "args-1",
			GateObservation:    "allow",
			ApprovalReceiptRef: "appr-1",
		},
		&kernel.ObserveToolApprovalCommand{
			CommandID:          "cmd-10",
			RunID:              testRunID,
			Origin:             testOrigin,
			Token:              11,
			ToolCallID:         testToolCallID,
			CheckGeneration:    1,
			Resolution:         "approved",
			ResolutionEvidence: "evidence-1",
		},
		&kernel.ObserveToolResultCommand{
			CommandID:      "cmd-11",
			RunID:          testRunID,
			Origin:         testOrigin,
			Token:          11,
			ToolCallID:     testToolCallID,
			AttemptID:      testAttemptID,
			Outcome:        "succeeded",
			ResultRef:      "res-1",
			Code:           "0",
			EffectEvidence: "effect-1",
			SourcePosition: 0,
		},
		&kernel.ReviseToolBindingCommand{
			CommandID:          "cmd-27",
			RunID:              testRunID,
			Origin:             testOrigin,
			OldRevision:        1,
			NewRevision:        2,
			AddedGroups:        []string{"file_ops"},
			RemovedGroups:      []string{"net_ops"},
			DescriptorVersions: map[string]string{"read_file": "v1", "write_file": "v2"},
			Cause:              "policy_update",
		},
	}
}

func sampleObservationCommands() []kernel.Command {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	return []kernel.Command{
		&kernel.ObserveVaultCommand{
			CommandID: "cmd-13",
			RunID:     testRunID,
			Origin:    testOrigin,
			Locked:    false,
			Evidence:  "vault_unlocked",
		},
		&kernel.ObserveHostPauseCommand{
			CommandID: "cmd-14",
			RunID:     testRunID,
			Origin:    testOrigin,
			Paused:    true,
			Evidence:  "quiescing",
		},
		&kernel.ObserveDependencyCommand{
			CommandID:       "cmd-15",
			RunID:           testRunID,
			Origin:          testOrigin,
			BlockerID:       "block-1",
			Kind:            kernel.WaitReasonDependency,
			Reference:       "dep-ref",
			ResumeCondition: "unblocked",
			Resolved:        false,
			Evidence:        "pending",
		},
		&kernel.TimerFiredCommand{
			CommandID:    "cmd-16",
			RunID:        testRunID,
			Origin:       testOrigin,
			TimerID:      "timer-1",
			Generation:   1,
			ObservedTime: now,
		},
	}
}

func sampleStopAndReconcileCommands() []kernel.Command {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	return []kernel.Command{
		&kernel.RequestStopCommand{
			CommandID:   "cmd-12",
			RunID:       testRunID,
			Origin:      testOrigin,
			Reason:      kernel.StopReasonUserCancel,
			Mode:        kernel.StopModeTerminal,
			Actor:       testAdmin,
			Target:      testRunID,
			RequestTime: now,
		},
		&kernel.ReconcileInvocationCommand{
			CommandID:        "cmd-18",
			RunID:            testRunID,
			Origin:           testOrigin,
			InvocationID:     testInvocationID,
			AttemptID:        testAttemptID,
			OperationReceipt: "rcpt-2",
			ObservedOutcome:  "reconciled",
		},
		&kernel.AcceptUnknownCommand{
			CommandID:           "cmd-19",
			RunID:               testRunID,
			Origin:              testOrigin,
			TargetAttempts:      []string{testAttemptID},
			Revisions:           []uint64{1},
			Actor:               testUser,
			AcceptedUncertainty: "unresolved",
			Disposition:         kernel.ControlDispositionClose,
		},
	}
}

func sampleContextAndInputCommands() []kernel.Command {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	return []kernel.Command{
		&kernel.CloseRunWithoutPayloadCommand{
			CommandID:                  "cmd-20",
			RunID:                      testRunID,
			Origin:                     testOrigin,
			MetadataOnlyPreviewVersion: "v1",
			RunRevision:                1,
			ExactAttemptIDs:            []string{testAttemptID},
			CauseIDs:                   []string{"cause-1"},
			Actor:                      testAdmin,
			UnavailabilityReason:       "vault_locked",
		},
		&kernel.SelectRunContextCommand{
			CommandID:          "cmd-21",
			RunID:              testRunID,
			Origin:             testOrigin,
			OldContextRevision: 1,
			NewContextRevision: 2,
			Epoch:              1,
			StartRunID:         testRunID,
			Mode:               "continue",
			Actor:              testUser,
			ValidationEvidence: "valid_hash",
		},
		&kernel.SteerInputCommand{
			CommandID: "cmd-25",
			RunID:     testRunID,
			Origin:    testOrigin,
			InputRef:  "steer-ref-1",
			Timestamp: now,
		},
		&kernel.InterjectInputCommand{
			CommandID:          "cmd-26",
			RunID:              testRunID,
			Origin:             testOrigin,
			InputRef:           "interject-ref-1",
			TargetAttemptID:    testAttemptID,
			TargetInvocationID: testInvocationID,
		},
	}
}

func allSampleCommands() []kernel.Command {
	cmds := make([]kernel.Command, 0, 27)
	cmds = append(cmds, sampleRunLifecycleCommands()...)
	cmds = append(cmds, sampleRunDispatchCommands()...)
	cmds = append(cmds, sampleModelCommands()...)
	cmds = append(cmds, sampleToolCommands()...)
	cmds = append(cmds, sampleObservationCommands()...)
	cmds = append(cmds, sampleStopAndReconcileCommands()...)
	cmds = append(cmds, sampleContextAndInputCommands()...)
	return cmds
}

func extractCommandName(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Name == nil || fn.Name.Name != "CommandName" || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "", false
	}
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return "", false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return "", false
	}
	lit, ok := ret.Results[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	return strings.Trim(lit.Value, `"`), true
}

func declaredKernelCommands(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "../kernel/commands.go", nil, 0)
	if err != nil {
		t.Fatalf("failed to parse kernel commands: %v", err)
	}
	var kinds []string
	for _, decl := range node.Decls {
		if kind, ok := extractCommandName(decl); ok {
			kinds = append(kinds, kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func TestCommandRoundTripAllTypes(t *testing.T) {
	t.Parallel()

	declared := declaredKernelCommands(t)
	commands := allSampleCommands()
	sampleKinds := make([]string, 0, len(commands))
	for _, cmd := range commands {
		sampleKinds = append(sampleKinds, cmd.CommandName())
	}
	slices.Sort(sampleKinds)

	if diff := cmp.Diff(declared, sampleKinds); diff != "" {
		t.Fatalf("declared commands and sample commands differ (-declared +sample):\n%s", diff)
	}

	for _, kind := range declared {
		if _, err := DecodeCommand(kind, []byte(`{}`)); err != nil {
			t.Fatalf("DecodeCommand failed to recognize declared kind %s: %v", kind, err)
		}
	}

	for _, cmd := range commands {
		kind := cmd.CommandName()
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			encoded, err := EncodeCommand(cmd)
			if err != nil {
				t.Fatalf("EncodeCommand failed: %v", err)
			}

			decoded, err := DecodeCommand(kind, encoded)
			if err != nil {
				t.Fatalf("DecodeCommand failed: %v", err)
			}

			if diff := cmp.Diff(cmd, decoded); diff != "" {
				t.Fatalf("decoded command mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeCommandErrors(t *testing.T) {
	t.Parallel()

	t.Run("unrecognized kind", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeCommand("NoSuchCommand", []byte(`{}`))
		if !errors.Is(err, ErrUnrecognizedCommand) {
			t.Fatalf("expected ErrUnrecognizedCommand, got %v", err)
		}
	})

	t.Run("duplicate key in command", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"CommandID":"cmd-1","CommandID":"cmd-2"}`)
		_, err := DecodeCommand("AcceptRun", data)
		if !errors.Is(err, ErrDuplicateKey) {
			t.Fatalf("expected ErrDuplicateKey, got %v", err)
		}
	})

	t.Run("unknown field in command", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"CommandID":"cmd-1","UnknownProperty":"bogus"}`)
		_, err := DecodeCommand("AcceptRun", data)
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("expected ErrUnknownField, got %v", err)
		}
	})

	t.Run("float number in command", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"CommandID":"cmd-1","GoalRound":3.14}`)
		_, err := DecodeCommand("AcceptRun", data)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Fatalf("expected ErrFloatNotAllowed, got %v", err)
		}
	})

	t.Run("truncated json in command", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"CommandID":`)
		_, err := DecodeCommand("AcceptRun", data)
		if !errors.Is(err, ErrMalformedJSON) {
			t.Fatalf("expected ErrMalformedJSON, got %v", err)
		}
	})

	t.Run("trailing data after command", func(t *testing.T) {
		t.Parallel()
		data := []byte(`{"CommandID":"cmd-1"} trailing`)
		_, err := DecodeCommand("AcceptRun", data)
		if !errors.Is(err, ErrTrailingData) {
			t.Fatalf("expected ErrTrailingData, got %v", err)
		}
	})
}
