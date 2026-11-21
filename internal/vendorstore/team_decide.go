package vendorstore

// F11.2b decision and amendment appends. Both apply their effects in one
// transaction — the team or task events, the re-sealed task specs and the
// inserted tasks — so an approved decision or amend is never recorded without
// the state it claims, and a failed append leaves the team untouched.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/sealing"
)

// ResealedTaskSpec replaces one task's sealed protected spec (F11.2b: the
// decided model or budget of a task is part of the sealed spec).
type ResealedTaskSpec struct {
	TaskID        string
	ProtectedSpec []byte
}

// TeamDecisionInput applies one approved decision (Q-98, OR-E13): TeamStarted
// moves the proposed team to active with the sealed decision payload, removed
// tasks cancel, edited tasks re-seal and added tasks insert — nothing starts.
type TeamDecisionInput struct {
	ExpectedSeq     int64
	DecisionEventID string
	StartedPayload  []byte
	RemovedTaskIDs  []string
	ResealedSpecs   []ResealedTaskSpec
	AddedTasks      []NewTaskInput
	// Mailbox rows are written in the same transaction as the TeamStarted
	// event, keyed to its sequence (Q-122, OR-E13 delivery).
	Mailbox []MailboxEnqueueInput
	// ArmTimers arms durable team timers (e.g. team_deadline) in the same
	// transaction as TeamStarted.
	ArmTimers []TeamTimerArmInput
}

// TeamAmendInput applies one user-approved amend (§5.4): TeamAmended keeps the
// active team active with the sealed amendment payload, edited tasks re-seal
// and added tasks insert as queued tasks that nothing starts (Q-98).
type TeamAmendInput struct {
	ExpectedSeq   int64
	AmendEventID  string
	AmendPayload  []byte
	ResealedSpecs []ResealedTaskSpec
	AddedTasks    []NewTaskInput
}

// DecideTeam applies an approved decision atomically and returns the team's
// new last sequence.
func (s *Store) DecideTeam(ctx context.Context, teamID string, in *TeamDecisionInput) (int64, error) {
	if in == nil || in.DecisionEventID == "" || len(in.StartedPayload) == 0 {
		return 0, fmt.Errorf("%w: a decision requires an event ID and a sealed payload", ErrTeamInvalidInput)
	}
	return s.applyTeamMutation(ctx, teamID, &AppendTeamEventInput{
		EventID: in.DecisionEventID, EventType: EventTeamStarted, ProtectedPayload: in.StartedPayload,
		Mailbox: in.Mailbox, ArmTimers: in.ArmTimers,
	}, in.ExpectedSeq, in.RemovedTaskIDs, in.ResealedSpecs, in.AddedTasks)
}

// AmendTeam applies a user-approved amendment atomically and returns the
// team's new last sequence.
func (s *Store) AmendTeam(ctx context.Context, teamID string, in *TeamAmendInput) (int64, error) {
	if in == nil || in.AmendEventID == "" || len(in.AmendPayload) == 0 {
		return 0, fmt.Errorf("%w: an amend requires an event ID and a sealed payload", ErrTeamInvalidInput)
	}
	// Q-116: a task an amend inserts starts only through its delegator.
	added := slices.Clone(in.AddedTasks)
	for i := range added {
		added[i].ExplicitStart = true
	}
	return s.applyTeamMutation(ctx, teamID, &AppendTeamEventInput{
		EventID: in.AmendEventID, EventType: EventTeamAmended, ProtectedPayload: in.AmendPayload,
	}, in.ExpectedSeq, nil, in.ResealedSpecs, added)
}

// applyTeamMutation runs one team mutation transaction: the team event, the
// task cancellations, the re-sealed specs and the inserted tasks. The
// transition table enforces the from-state, so a decision on a no-longer
// proposed team or an amend on a non-active team fails typed and appends
// nothing.
func (s *Store) applyTeamMutation(
	ctx context.Context,
	teamID string,
	ev *AppendTeamEventInput,
	expectedSeq int64,
	removed []string,
	reseals []ResealedTaskSpec,
	added []NewTaskInput,
) (int64, error) {
	if teamID == "" || ev.EventID == "" || expectedSeq < 1 {
		return 0, fmt.Errorf("%w: team_id, event_id and positive expected_seq are required", ErrTeamInvalidInput)
	}
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin team mutation tx: %w", err)
	}
	defer rollback(ctx, tx)

	meta, err := loadTeamMetaForUpdate(ctx, tx, teamID, expectedSeq)
	if err != nil {
		return 0, err
	}
	rawKey, err := s.unwrapTeamKey(teamID, meta.KeyID, meta.WrappedKey)
	if err != nil {
		return 0, err
	}
	defer clear(rawKey)

	lastSeq, err := s.appendTeamEventAndRemovals(ctx, tx, teamID, ev, meta, rawKey, removed, expectedSeq)
	if err != nil {
		return 0, err
	}
	if err := s.resealAndInsertTasks(ctx, tx, teamID, meta, rawKey, reseals, added, &lastSeq); err != nil {
		return 0, err
	}
	if err := s.commitTeamTx(ctx, teamID, lastSeq, snap, true, tx.Commit); err != nil {
		return 0, err
	}
	return lastSeq, nil
}

// appendTeamEventAndRemovals appends the mutation's team event and cancels
// the removed tasks, returning the team's last sequence.
func (s *Store) appendTeamEventAndRemovals(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	ev *AppendTeamEventInput,
	meta teamMetaRow,
	rawKey []byte,
	removed []string,
	expectedSeq int64,
) (int64, error) {
	lastSeq := expectedSeq + 1
	now := s.clock.Now().UnixMilli()
	fromState, toState, err := applyTeamProjection(ctx, tx, teamID, meta.State, lastSeq, now, ev)
	if err != nil {
		return 0, err
	}
	envelope, err := sealing.SealPayload(randomReader{s.random}, teamEventRecordType, teamID, ev.EventID, string(ev.EventType), meta.KeyID, rawKey, ev.ProtectedPayload)
	if err != nil {
		return 0, fmt.Errorf("seal the team event payload: %w", err)
	}
	if insertErr := insertAppendedTeamEvent(ctx, tx, teamID, lastSeq, ev, fromState, toState, envelope, now); insertErr != nil {
		return 0, insertErr
	}
	if len(ev.Mailbox) > 0 {
		if mailboxErr := s.insertMailboxRows(ctx, tx, teamID, lastSeq, ev.Mailbox, meta.KeyID, rawKey, now); mailboxErr != nil {
			return 0, mailboxErr
		}
	}
	if len(ev.ArmTimers) > 0 {
		if timerErr := armTeamTimersTx(ctx, tx, teamID, ev.ArmTimers, now); timerErr != nil {
			return 0, timerErr
		}
	}
	for _, taskID := range removed {
		if lastSeq, err = s.appendTaskCancellation(ctx, tx, teamID, taskID, cancelEventID(ev.EventID, taskID), lastSeq+1, now); err != nil {
			return 0, err
		}
	}
	return lastSeq, nil
}

// resealAndInsertTasks replaces the sealed specs of the edited tasks and
// inserts the added ones after the team's last sequence, advancing lastSeq
// past their TaskCreated events.
func (s *Store) resealAndInsertTasks(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	meta teamMetaRow,
	rawKey []byte,
	reseals []ResealedTaskSpec,
	added []NewTaskInput,
	lastSeq *int64,
) error {
	now := s.clock.Now().UnixMilli()
	for i := range reseals {
		if err := s.resealTaskSpec(ctx, tx, teamID, &reseals[i], meta.KeyID, rawKey, now); err != nil {
			return err
		}
	}
	if len(added) == 0 {
		return nil
	}
	existing, err := queryTeamTasks(ctx, tx, teamID)
	if err != nil {
		return err
	}
	depths, err := validateTaskInsertions(existing, added)
	if err != nil {
		return err
	}
	records, err := s.insertTasksAt(ctx, tx, teamID, added, depths, *lastSeq+1, meta.KeyID, rawKey, now)
	if err != nil {
		return err
	}
	*lastSeq += int64(len(added))
	return bumpTeamAfterInsert(ctx, tx, teamID, *lastSeq, len(records), now)
}

// appendTaskCancellation cancels one still-queued task through its task
// transition (Q-84: an unknown task is not settled, but a decision removes
// only tasks a proposal created, which are queued). It returns the sequence
// the cancellation used.
func (s *Store) appendTaskCancellation(ctx context.Context, tx *sql.Tx, teamID, taskID, eventID string, seq, now int64) (int64, error) {
	ev := &AppendTeamEventInput{EventID: eventID, TaskID: taskID, EventType: EventTaskEnded, Outcome: outcomeCancelled}
	if err := s.appendTaskEventAndCancel(ctx, tx, teamID, taskID, ev, seq, now); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) appendTaskEventAndCancel(ctx context.Context, tx *sql.Tx, teamID, taskID string, ev *AppendTeamEventInput, seq, now int64) error {
	fromState, toState, err := applyTaskProjection(ctx, tx, teamID, seq, now, ev)
	if err != nil {
		return err
	}
	if err := insertAppendedTeamEvent(ctx, tx, teamID, seq, ev, fromState, toState, nil, now); err != nil {
		return err
	}
	return cancelTaskTimersTx(ctx, tx, teamID, taskID, now)
}

// cancelEventID derives the event ID of one task cancellation from the
// mutation's own event ID: event IDs are unique per team, so removing two
// tasks in one transaction needs distinct IDs.
func cancelEventID(parentEventID, taskID string) string {
	return parentEventID + ".cancel." + taskID
}

// resealTaskSpec replaces one task's sealed spec; a task that does not exist
// fails typed instead of silently skipping.
func (s *Store) resealTaskSpec(ctx context.Context, tx *sql.Tx, teamID string, reseal *ResealedTaskSpec, keyID string, rawKey []byte, now int64) error {
	if reseal.TaskID == "" || len(reseal.ProtectedSpec) == 0 {
		return fmt.Errorf("%w: a re-sealed spec requires a task ID and a payload", ErrTeamInvalidInput)
	}
	envelope, err := sealing.SealPayload(randomReader{s.random}, teamSpecRecordType, teamID, reseal.TaskID, "spec", keyID, rawKey, reseal.ProtectedSpec)
	if err != nil {
		return fmt.Errorf("seal the replaced task spec: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE team_tasks SET spec_envelope = ?, updated_at = ?
		WHERE team_id = ? AND task_id = ?`,
		envelope, now, teamID, reseal.TaskID,
	)
	if err != nil {
		return fmt.Errorf("replace the task spec: %w", err)
	}
	if affected, aerr := res.RowsAffected(); aerr == nil && affected == 0 {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, reseal.TaskID)
	}
	return nil
}

// bumpTeamAfterInsert advances the team projection after an insertion: the
// inserted TaskCreated events moved last_seq and each new row adds to
// task_count.
func bumpTeamAfterInsert(ctx context.Context, tx *sql.Tx, teamID string, lastSeq int64, addedCount int, now int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE team_records SET last_seq = ?, task_count = task_count + ?, updated_at = ?
		WHERE team_id = ?`,
		lastSeq, addedCount, now, teamID,
	)
	if err != nil {
		return fmt.Errorf("bump the team after insert: %w", err)
	}
	return nil
}

// insertionNode is one task of the combined existing-plus-new graph the
// insertion validation walks.
type insertionNode struct {
	role    TeamRole
	parent  string
	depth   int
	deps    []string
	fromNew bool
}

// validateTaskInsertions checks new tasks against the team's existing rows and
// each other: the §4 contract, the depth-2 hierarchy with valid parent
// pairings, and a depends_on DAG over existing and new edges (§5.4 additions).
// It returns the depth of every new task.
func validateTaskInsertions(existing []TaskRecord, tasks []NewTaskInput) (map[string]int, error) {
	combined, err := combinedInsertionNodes(existing, tasks)
	if err != nil {
		return nil, err
	}
	depths, err := insertionDepths(combined, tasks)
	if err != nil {
		return nil, err
	}
	if err := validateInsertionPairings(combined, tasks, depths); err != nil {
		return nil, err
	}
	if err := validateInsertionDependencies(combined, tasks); err != nil {
		return nil, err
	}
	return depths, nil
}

// combinedInsertionNodes builds the combined existing-plus-new graph,
// rejecting ID collisions, invalid fields and a solo task joining a team.
func combinedInsertionNodes(existing []TaskRecord, tasks []NewTaskInput) (map[string]insertionNode, error) {
	combined := make(map[string]insertionNode, len(existing)+len(tasks))
	for i := range existing {
		e := &existing[i]
		combined[e.TaskID] = insertionNode{role: e.HierarchyRole, parent: e.ParentTaskID, depth: e.Depth, deps: e.DependsOn}
	}
	for i := range tasks {
		t := &tasks[i]
		if _, clash := combined[t.TaskID]; clash {
			return nil, fmt.Errorf("%w: %s", ErrTaskExists, t.TaskID)
		}
		if err := validateTaskInputFields(t, nil); err != nil {
			return nil, err
		}
		if t.HierarchyRole == RoleSolo {
			return nil, fmt.Errorf("%w: a solo task cannot join an existing team", ErrTeamInvalidInput)
		}
		combined[t.TaskID] = insertionNode{role: t.HierarchyRole, parent: t.ParentTaskID, deps: append([]string{}, t.DependsOn...), fromNew: true}
	}
	return combined, nil
}

// insertionDepths computes the depth of every new task over the graph.
func insertionDepths(combined map[string]insertionNode, tasks []NewTaskInput) (map[string]int, error) {
	depths := make(map[string]int, len(tasks))
	for i := range tasks {
		depth, err := insertionDepth(combined, tasks[i].TaskID)
		if err != nil {
			return nil, err
		}
		depths[tasks[i].TaskID] = depth
	}
	return depths, nil
}

// validateInsertionPairings rejects a new task whose parent pairing or depth
// breaks the OR-01 hierarchy.
func validateInsertionPairings(combined map[string]insertionNode, tasks []NewTaskInput, depths map[string]int) error {
	for i := range tasks {
		t := &tasks[i]
		if t.ParentTaskID == "" {
			continue
		}
		if !validParentRolePair(combined[t.ParentTaskID].role, t.HierarchyRole, depths[t.TaskID]) {
			return fmt.Errorf("%w: invalid parent/child role pairing", ErrTeamInvalidInput)
		}
	}
	return nil
}

// insertionDepth computes one new task's depth by walking its parent chain;
// existing tasks carry their projected depth, a new one starts at zero.
func insertionDepth(combined map[string]insertionNode, taskID string) (int, error) {
	depth := 0
	cur := combined[taskID]
	for cur.parent != "" {
		parent, known := combined[cur.parent]
		if !known {
			return 0, fmt.Errorf("%w: missing parent %s", ErrTeamInvalidInput, cur.parent)
		}
		if parent.fromNew {
			depth++
			cur = parent
			continue
		}
		return depth + parent.depth + 1, nil
	}
	if cur.role != RoleLead {
		return 0, fmt.Errorf("%w: a root task must be a lead", ErrTeamInvalidInput)
	}
	return depth, nil
}

// validateInsertionDependencies checks every depends_on target of the new
// tasks against the combined graph and rejects cycles the new edges create.
func validateInsertionDependencies(combined map[string]insertionNode, tasks []NewTaskInput) error {
	if err := validateInsertionTargets(combined, tasks); err != nil {
		return err
	}
	return detectInsertionCycles(combined, tasks)
}

// validateInsertionTargets rejects an empty, self, duplicate or missing
// depends_on target.
func validateInsertionTargets(combined map[string]insertionNode, tasks []NewTaskInput) error {
	for i := range tasks {
		t := &tasks[i]
		seen := make(map[string]struct{}, len(t.DependsOn))
		for _, dep := range t.DependsOn {
			if dep == "" || dep == t.TaskID {
				return fmt.Errorf("%w: self or empty depends_on in %s", ErrTeamInvalidInput, t.TaskID)
			}
			if _, known := combined[dep]; !known {
				return fmt.Errorf("%w: depends_on target %s not in team", ErrTeamInvalidInput, dep)
			}
			if _, dup := seen[dep]; dup {
				return fmt.Errorf("%w: duplicate depends_on in %s", ErrTeamInvalidInput, t.TaskID)
			}
			seen[dep] = struct{}{}
		}
	}
	return nil
}

// detectInsertionCycles walks the combined graph from every new task and
// rejects a depends_on cycle the new edges create.
func detectInsertionCycles(combined map[string]insertionNode, tasks []NewTaskInput) error {
	state := make(map[string]int, len(combined))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("%w: depends_on cycle at %s", ErrTeamInvalidInput, id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dep := range combined[id].deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for i := range tasks {
		if err := visit(tasks[i].TaskID); err != nil {
			return err
		}
	}
	return nil
}

// insertTasksAt inserts new tasks with their TaskCreated events exactly the
// way CreateTeam creates its initial tasks: sealed spec per row, TaskCreated
// event per task at the team's next sequence. It returns the projected rows.
func (s *Store) insertTasksAt(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	tasks []NewTaskInput,
	depths map[string]int,
	startSeq int64,
	keyID string,
	rawKey []byte,
	now int64,
) ([]TaskRecord, error) {
	records := make([]TaskRecord, 0, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		seq := startSeq + int64(i)
		specEnv, err := sealing.SealPayload(randomReader{s.random}, teamSpecRecordType, teamID, t.TaskID, "spec", keyID, rawKey, t.ProtectedSpec)
		if err != nil {
			return nil, fmt.Errorf("seal task spec: %w", err)
		}
		evEnv, err := sealing.SealPayload(randomReader{s.random}, teamEventRecordType, teamID, t.CreatedEventID, string(EventTaskCreated), keyID, rawKey, t.ProtectedSpec)
		if err != nil {
			return nil, fmt.Errorf("seal TaskCreated payload: %w", err)
		}
		depsJSON, err := json.Marshal(append([]string{}, t.DependsOn...))
		if err != nil {
			return nil, fmt.Errorf("marshal depends_on: %w", err)
		}
		if err := insertTaskAndCreatedEvent(ctx, tx, teamID, t, depths[t.TaskID], seq, depsJSON, specEnv, evEnv, now); err != nil {
			return nil, err
		}
		records = append(records, TaskRecord{
			TaskID: t.TaskID, TeamID: teamID, ParentTaskID: t.ParentTaskID,
			HierarchyRole: t.HierarchyRole, CatalogRole: t.CatalogRole, Deliverable: t.Deliverable,
			DependsOn: t.DependsOn, Depth: depths[t.TaskID], State: TaskStateQueued,
			LastEventSeq: seq, CreatedAt: now, UpdatedAt: now,
		})
	}
	return records, nil
}
