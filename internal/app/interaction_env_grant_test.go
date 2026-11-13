package app

import (
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/environments"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const envGrantWorkspaceID = "ws-env-grant"

// TestIE03GrantInvalidatedByRegistrySetWithoutCallerInvolvement proves the
// grant revision comes from the real tag registry: the same Binding value is
// reused across Registry.Set calls and the grant stops covering it, including
// after the policy is reverted.
func TestIE03GrantInvalidatedByRegistrySetWithoutCallerInvolvement(t *testing.T) {
	t.Parallel()

	reg := environments.New()
	if err := reg.Set("sandbox", false); err != nil {
		t.Fatal(err)
	}
	owner, err := interaction.NewOwner(&interaction.Config{
		Clock:     clock.NewFake(time.Unix(1700000000, 0).UTC()),
		IDs:       ids.NewSequence("env"),
		Responder: interaction.NopResponder{},
		TagPolicy: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := &interaction.Spec{
		OperationID: "op-ie03-registry",
		Kind:        interaction.KindConfirm,
		Summary:     "Write main.go",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID:     "sess-1",
			WorkspaceID:   envGrantWorkspaceID,
			ToolName:      "edit",
			GrantScopeKey: "edit:dir:src/",
			HasTarget:     true,
			EnvTag:        "sandbox",
		},
	}
	ticket, _, err := owner.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Answer(&interaction.Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     interaction.OutcomeAllowedSession,
	}); err != nil {
		t.Fatal(err)
	}
	if !owner.HasBindingGrant(&spec.Binding) {
		t.Fatal("grant missing right after allowed_session")
	}
	for _, requires := range []bool{true, false} {
		if err := reg.Set("sandbox", requires); err != nil {
			t.Fatal(err)
		}
		if owner.HasBindingGrant(&spec.Binding) {
			t.Fatalf("grant survived Registry.Set(sandbox, %v)", requires)
		}
	}
}
