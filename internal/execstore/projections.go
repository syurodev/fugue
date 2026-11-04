package execstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/kernel"
)

func writeRunProjection(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	state *kernel.State,
	newRunRevision uint64,
) error {
	waitReasons := kernel.DeriveWaitReasons(state)
	reasonsJSON, err := json.Marshal(waitReasons)
	if err != nil {
		return fmt.Errorf("%w: marshal wait reasons: %w", ErrInvalidBatch, err)
	}

	admissionToken := state.AdmissionToken
	if req.AdmissionFence != nil {
		admissionToken = req.AdmissionFence.Token
	}

	_, err = conn.ExecContext(
		ctx,
		`INSERT INTO exec_runs (
			run_id, session_id, last_sequence, revision, protocol_version,
			lifecycle, phase, wait_reasons, outcome, admission_token, step_index, context_epoch
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			last_sequence = excluded.last_sequence,
			revision = excluded.revision,
			protocol_version = excluded.protocol_version,
			lifecycle = excluded.lifecycle,
			phase = excluded.phase,
			wait_reasons = excluded.wait_reasons,
			outcome = excluded.outcome,
			admission_token = excluded.admission_token,
			step_index = excluded.step_index,
			context_epoch = excluded.context_epoch`,
		req.RunID,
		req.SessionID,
		state.LastSequence,
		newRunRevision,
		CurrentProtocolVersion,
		string(kernel.DeriveLifecycle(state)),
		string(kernel.DerivePhase(state)),
		string(reasonsJSON),
		string(state.Outcome),
		admissionToken,
		state.CurrentStepIndex,
		state.ContextEpoch,
	)
	if err != nil {
		return fmt.Errorf("%w: write exec_runs projection: %w", ErrInvalidBatch, err)
	}
	return nil
}

func writeInvocationProjections(ctx context.Context, conn *sql.Conn, runID string, state *kernel.State) error {
	for invID, inv := range state.ModelInvocations {
		if err := writeModelInvocation(ctx, conn, runID, invID, inv); err != nil {
			return err
		}
	}
	for toolID, inv := range state.ToolInvocations {
		if err := writeToolInvocation(ctx, conn, runID, toolID, inv); err != nil {
			return err
		}
	}
	return nil
}

func writeModelInvocation(
	ctx context.Context,
	conn *sql.Conn,
	runID, invID string,
	inv *kernel.ModelInvocation,
) error {
	stateStr := "open"
	switch {
	case inv.Closed:
		stateStr = "closed"
	case inv.Completed:
		stateStr = "completed"
	case inv.Failed:
		stateStr = "failed"
	}

	_, err := conn.ExecContext(
		ctx,
		`INSERT INTO exec_invocations (
			run_id, invocation_id, kind, state, selected_attempt_id,
			effect_class, descriptor_version, check_generation, source_position, exposure
		) VALUES (?, ?, 'model', ?, ?, '', '', 0, 0, 'none')
		ON CONFLICT(run_id, invocation_id) DO UPDATE SET
			state = excluded.state,
			selected_attempt_id = excluded.selected_attempt_id`,
		runID, invID, stateStr, inv.SelectedAttemptID,
	)
	if err != nil {
		return fmt.Errorf("%w: write model invocation: %w", ErrInvalidBatch, err)
	}

	for attemptID, att := range inv.Attempts {
		status := string(att.Status)
		if status == "" {
			status = "planned"
		}
		_, err := conn.ExecContext(
			ctx,
			`INSERT INTO exec_attempts (
				run_id, attempt_id, invocation_id, status,
				owner_incarnation, executor_fence, exposure_unknown
			) VALUES (?, ?, ?, ?, ?, 0, 0)
			ON CONFLICT(run_id, attempt_id) DO UPDATE SET
				status = excluded.status,
				owner_incarnation = excluded.owner_incarnation`,
			runID, attemptID, invID, status, att.OwnerIncarnation,
		)
		if err != nil {
			return fmt.Errorf("%w: write model attempt: %w", ErrInvalidBatch, err)
		}
	}
	return nil
}

func writeToolInvocation(
	ctx context.Context,
	conn *sql.Conn,
	runID, toolID string,
	inv *kernel.ToolInvocation,
) error {
	stateStr := "open"
	if inv.Closed {
		stateStr = "closed"
	}

	_, err := conn.ExecContext(
		ctx,
		`INSERT INTO exec_invocations (
			run_id, invocation_id, kind, state, selected_attempt_id,
			effect_class, descriptor_version, check_generation, source_position, exposure
		) VALUES (?, ?, 'tool', ?, ?, ?, ?, ?, ?, 'none')
		ON CONFLICT(run_id, invocation_id) DO UPDATE SET
			state = excluded.state,
			selected_attempt_id = excluded.selected_attempt_id,
			effect_class = excluded.effect_class,
			descriptor_version = excluded.descriptor_version,
			check_generation = excluded.check_generation,
			source_position = excluded.source_position`,
		runID, toolID, stateStr, inv.SelectedAttemptID,
		string(inv.EffectClass), inv.DescriptorVersion,
		inv.CheckGeneration, inv.SourcePosition,
	)
	if err != nil {
		return fmt.Errorf("%w: write tool invocation: %w", ErrInvalidBatch, err)
	}

	for attemptID, att := range inv.Attempts {
		status := string(att.Status)
		if status == "" {
			status = "planned"
		}
		exposureInt := 0
		if att.ExposureUnknown {
			exposureInt = 1
		}
		_, err := conn.ExecContext(
			ctx,
			`INSERT INTO exec_attempts (
				run_id, attempt_id, invocation_id, status,
				owner_incarnation, executor_fence, exposure_unknown
			) VALUES (?, ?, ?, ?, ?, 0, ?)
			ON CONFLICT(run_id, attempt_id) DO UPDATE SET
				status = excluded.status,
				owner_incarnation = excluded.owner_incarnation,
				exposure_unknown = excluded.exposure_unknown`,
			runID, attemptID, toolID, status, att.OwnerIncarnation, exposureInt,
		)
		if err != nil {
			return fmt.Errorf("%w: write tool attempt: %w", ErrInvalidBatch, err)
		}
	}
	return nil
}

func writeTranscriptProjections(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	state *kernel.State,
) error {
	var maxPos int
	if err := conn.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(position), 0) FROM exec_transcript WHERE session_id = ? AND context_epoch = ?",
		req.SessionID, state.ContextEpoch,
	).Scan(&maxPos); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: query transcript max position: %w", ErrStoreUnavailable, err)
	}

	nextPos := maxPos
	for _, rawEvt := range req.Events {
		entry := extractTranscriptEntry(ctx, conn, req, rawEvt)
		if entry == nil {
			continue
		}
		nextPos++
		_, err := conn.ExecContext(
			ctx,
			`INSERT INTO exec_transcript (
				session_id, context_epoch, position, run_id, role,
				call_id, source_position, payload_id, disposition, event_sequence
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			req.SessionID, state.ContextEpoch, nextPos, req.RunID,
			entry.role, entry.callID, entry.sourcePos, entry.payloadID,
			entry.disposition, entry.sequence,
		)
		if err != nil {
			return fmt.Errorf("%w: write exec_transcript: %w", ErrInvalidBatch, err)
		}
	}
	return nil
}

type transcriptEntry struct {
	role        string
	callID      string
	sourcePos   int
	payloadID   *string
	disposition string
	sequence    uint64
}

func extractAcceptedTranscript(req *AppendRequest, e *kernel.RunAcceptedEvent) *transcriptEntry {
	if req.QueueFence != nil {
		return nil
	}
	var pID *string
	disp := ""
	if e.InputRef != "" {
		p := e.InputRef
		pID = &p
	} else {
		disp = "accepted"
	}
	return &transcriptEntry{
		role:        "input",
		callID:      "",
		sourcePos:   0,
		payloadID:   pID,
		disposition: disp,
		sequence:    e.Envelope.Sequence,
	}
}

func extractAdmittedTranscript(ctx context.Context, conn *sql.Conn, req *AppendRequest, e *kernel.RunAdmittedEvent) *transcriptEntry {
	var pID *string
	if req.ProtectedInputID != "" {
		p := req.ProtectedInputID
		pID = &p
	} else {
		var prevPayload sql.NullString
		scanErr := conn.QueryRowContext(ctx, "SELECT payload_id FROM exec_events WHERE run_id = ? AND event_type = 'RunAccepted'", req.RunID).Scan(&prevPayload)
		if scanErr == nil && prevPayload.Valid && prevPayload.String != "" {
			p := prevPayload.String
			pID = &p
		}
	}
	disp := ""
	if pID == nil {
		disp = "admitted"
	}
	return &transcriptEntry{
		role:        "input",
		callID:      "",
		sourcePos:   0,
		payloadID:   pID,
		disposition: disp,
		sequence:    e.Envelope.Sequence,
	}
}

func extractTranscriptEntry(ctx context.Context, conn *sql.Conn, req *AppendRequest, rawEvt kernel.Event) *transcriptEntry {
	switch e := rawEvt.(type) {
	case *kernel.RunAcceptedEvent:
		return extractAcceptedTranscript(req, e)
	case *kernel.RunAdmittedEvent:
		return extractAdmittedTranscript(ctx, conn, req, e)
	case *kernel.ModelResponseRecordedEvent:
		var pID *string
		disp := ""
		if e.ResponseRef != "" {
			p := e.ResponseRef
			pID = &p
		} else {
			disp = "recorded"
		}
		return &transcriptEntry{
			role:        "assistant",
			callID:      "",
			sourcePos:   0,
			payloadID:   pID,
			disposition: disp,
			sequence:    e.Envelope.Sequence,
		}
	case *kernel.ToolResultRecordedEvent:
		var pID *string
		disp := string(e.Outcome)
		if disp == "" {
			disp = "completed"
		}
		if e.ResultRef != "" {
			p := e.ResultRef
			pID = &p
		}
		return &transcriptEntry{
			role:        "tool_result",
			callID:      e.ToolCallID,
			sourcePos:   e.SourcePosition,
			payloadID:   pID,
			disposition: disp,
			sequence:    e.Envelope.Sequence,
		}
	default:
		return nil
	}
}
