package app

// The durable edits of an approve-with-edits decision: the tasks the user
// removed, the sealed specs the user changed, and the tasks the user added
// (OR-E13). Everything re-derives from the sealed plan and the edited plan.

import (
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// decisionMutations collects the durable edits of an approve-with-edits
// decision: the tasks the user removed, the sealed specs the user changed,
// and the tasks the user added.
type decisionMutations struct {
	removed []string
	reseals []vendorstore.ResealedTaskSpec
	added   []vendorstore.NewTaskInput
}

func (s *TeamProposalService) decisionMutations(teamID string, input, edited *teams.ProposalInput) (*decisionMutations, error) {
	out := &decisionMutations{}
	if edited == nil {
		// A plain approve carries no edits: nothing is removed, re-sealed
		// or inserted (OR-E13).
		return out, nil
	}
	out.removed = removedTaskIDs(teamID, input, edited)
	reseals, err := changedTaskSpecs(teamID, input, edited)
	if err != nil {
		return nil, err
	}
	out.reseals = reseals
	added, err := s.addedTaskInputs(teamID, input, edited)
	if err != nil {
		return nil, err
	}
	out.added = added
	return out, nil
}

// removedTaskIDs lists the durable IDs of the tasks the edited plan dropped.
func removedTaskIDs(teamID string, input, edited *teams.ProposalInput) []string {
	surviving := make(map[string]struct{}, len(edited.Tasks))
	for i := range edited.Tasks {
		surviving[edited.Tasks[i].ID] = struct{}{}
	}
	var removed []string
	for i := range input.Tasks {
		if _, kept := surviving[input.Tasks[i].ID]; !kept {
			removed = append(removed, durableTaskID(teamID, input.Tasks[i].ID))
		}
	}
	return removed
}

// changedTaskSpecs lists the re-sealed specs of the surviving tasks whose
// protected content the edits changed.
func changedTaskSpecs(teamID string, input, edited *teams.ProposalInput) ([]vendorstore.ResealedTaskSpec, error) {
	original := make(map[string]*teams.ProposedTask, len(input.Tasks))
	for i := range input.Tasks {
		original[input.Tasks[i].ID] = &input.Tasks[i]
	}
	var reseals []vendorstore.ResealedTaskSpec
	for i := range edited.Tasks {
		t := &edited.Tasks[i]
		before, existed := original[t.ID]
		if !existed {
			continue
		}
		oldSpec, newSpec := before.TaskSpec(), t.TaskSpec()
		if sameProtectedContent(&oldSpec, &newSpec) {
			continue
		}
		specJSON, marshalErr := json.Marshal(newSpec.ProtectedContent())
		if marshalErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "marshal the edited task spec", marshalErr)
		}
		reseals = append(reseals, vendorstore.ResealedTaskSpec{TaskID: durableTaskID(teamID, t.ID), ProtectedSpec: specJSON})
	}
	return reseals, nil
}

// addedTaskInputs maps the tasks the edited plan introduced onto store
// insert inputs.
func (s *TeamProposalService) addedTaskInputs(teamID string, input, edited *teams.ProposalInput) ([]vendorstore.NewTaskInput, error) {
	original := make(map[string]struct{}, len(input.Tasks))
	for i := range input.Tasks {
		original[input.Tasks[i].ID] = struct{}{}
	}
	var added []vendorstore.NewTaskInput
	for i := range edited.Tasks {
		if _, existed := original[edited.Tasks[i].ID]; existed {
			continue
		}
		taskInput, err := newTaskInputOf(teamID, &edited.Tasks[i], s.ids)
		if err != nil {
			return nil, err
		}
		added = append(added, *taskInput)
	}
	return added, nil
}

// sameProtectedContent compares two sealed spec contents by their encoding.
func sameProtectedContent(a, b *teams.TaskSpec) bool {
	aJSON, aErr := json.Marshal(a.ProtectedContent())
	bJSON, bErr := json.Marshal(b.ProtectedContent())
	return aErr == nil && bErr == nil && string(aJSON) == string(bJSON)
}
