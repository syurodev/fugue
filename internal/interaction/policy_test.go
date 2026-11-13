package interaction

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const (
	tagCaseDev      = "dev"
	tagCaseProd     = "prod"
	tagCaseEmpty    = "empty"
	tagCaseUnknown  = "unknown"
	tagCaseNoTarget = "no_target"
)

func ie01TargetRequiresConfirm(tagCase string) bool {
	switch tagCase {
	case tagCaseDev, tagCaseNoTarget:
		return false
	case tagCaseProd, tagCaseEmpty, tagCaseUnknown:
		return true
	default:
		return true
	}
}

func ie01Step1(confirm Confirm, risk Risk, targetRequiresConfirm bool) bool {
	if confirm == ConfirmAlways {
		return true
	}
	return (confirm == ConfirmEnvTag || confirm == ConfirmCustom) && targetRequiresConfirm && risk != RiskRead
}

func ie01SessionOrAsk(hasGrant, escalation bool) Verdict {
	if hasGrant {
		return Verdict{Kind: VerdictRun, Outcome: OutcomeAllowedSession}
	}
	return Verdict{
		Kind:       VerdictAsk,
		Offers:     []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession},
		Escalation: escalation,
	}
}

// expectedIE01Verdict calculates the reference verdict for I-E01 combinations
// via an independent handcrafted lookup table logic (I-E01).
func expectedIE01Verdict(
	mode ApprovalMode,
	risk Risk,
	confirm Confirm,
	tagCase string,
	hasGrant, escalation bool,
) Verdict {
	targetRequiresConfirm := ie01TargetRequiresConfirm(tagCase)

	// Step 1: Hard rule I-03
	if ie01Step1(confirm, risk, targetRequiresConfirm) {
		return Verdict{
			Kind:   VerdictAsk,
			Offers: []Outcome{OutcomeAllowedOnce},
		}
	}

	// Step 2: Mode Plan and risk != read
	if mode == ModePlan && risk != RiskRead {
		return Verdict{
			Kind:   VerdictRefuse,
			Reason: RefusePlanMode,
		}
	}

	// Step 3: Escalation
	if escalation {
		return ie01SessionOrAsk(hasGrant, true)
	}

	// Step 4: Mode Full
	if mode == ModeFull {
		return Verdict{Kind: VerdictRun}
	}

	// Step 5: Mode Auto
	if mode == ModeAuto && (risk == RiskDestructive || risk == RiskExternal) {
		return ie01SessionOrAsk(hasGrant, false)
	}

	// Step 6: Mode Ask
	if mode == ModeAsk && risk != RiskRead {
		return ie01SessionOrAsk(hasGrant, false)
	}

	// Step 7: Remainder
	return Verdict{Kind: VerdictRun}
}

type ie01Case struct {
	mode       ApprovalMode
	risk       Risk
	confirm    Confirm
	tagCase    string
	hasGrant   bool
	escalation bool
}

func buildIE01Cases() []ie01Case {
	modes := []ApprovalMode{ModePlan, ModeAsk, ModeAuto, ModeFull}
	risks := []Risk{RiskRead, RiskWrite, RiskDestructive, RiskExternal}
	confirms := []Confirm{ConfirmNever, ConfirmEnvTag, ConfirmAlways, ConfirmCustom}
	tagCases := []string{tagCaseDev, tagCaseProd, tagCaseEmpty, tagCaseUnknown, tagCaseNoTarget}

	const totalCases = 4 * 4 * 4 * 5 * 2 * 2
	cases := make([]ie01Case, totalCases)
	for i := range totalCases {
		idx := i
		esc := idx%2 == 1
		idx /= 2
		hasGrant := idx%2 == 1
		idx /= 2
		tagCase := tagCases[idx%len(tagCases)]
		idx /= len(tagCases)
		confirm := confirms[idx%len(confirms)]
		idx /= len(confirms)
		risk := risks[idx%len(risks)]
		idx /= len(risks)
		mode := modes[idx%len(modes)]

		cases[i] = ie01Case{
			mode:       mode,
			risk:       risk,
			confirm:    confirm,
			tagCase:    tagCase,
			hasGrant:   hasGrant,
			escalation: esc,
		}
	}
	return cases
}

func runIE01Case(t *testing.T, tags TagPolicy, tc ie01Case) {
	t.Helper()
	hasTarget := tc.tagCase != tagCaseNoTarget
	envTag := ""
	switch tc.tagCase {
	case tagCaseDev:
		envTag = testEnvDev
	case tagCaseProd:
		envTag = testEnvProd
	case tagCaseUnknown:
		envTag = "unknown-tag"
	}

	const scopeKey = "test:scope:key"
	grantLookup := GrantLookupFunc(func(key string) bool {
		return tc.hasGrant && key == scopeKey
	})

	descriptor := Descriptor{Risk: tc.risk, Confirm: tc.confirm}
	call := Call{
		GrantScopeKey:   scopeKey,
		EnvTag:          envTag,
		HasTarget:       hasTarget,
		NeedsEscalation: tc.escalation,
	}
	if tc.confirm == ConfirmCustom {
		call.EffectiveRisk = tc.risk
		call.EffectiveConfirm = tc.confirm
	}

	got, err := Decide(tc.mode, descriptor, call, tags, grantLookup)
	if err != nil {
		t.Fatalf("unexpected Decide error: %v", err)
	}

	want := expectedIE01Verdict(tc.mode, tc.risk, tc.confirm, tc.tagCase, tc.hasGrant, tc.escalation)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Decide mismatch (-want +got)\n%s", diff)
	}
}

func TestIE01DecideTableAllCombinations(t *testing.T) {
	t.Parallel()

	tags := newStubTagPolicy()
	cases := buildIE01Cases()
	for _, tc := range cases {
		runIE01Case(t, tags, tc)
	}

	const expectedTotal = 4 * 4 * 4 * 5 * 2 * 2
	if len(cases) != expectedTotal {
		t.Fatalf("ran %d combinations, want %d", len(cases), expectedTotal)
	}
}

func testIE02HardRuleDecisions(t *testing.T, tags TagPolicy, grants GrantLookup) {
	t.Helper()
	callProd := Call{
		GrantScopeKey: "tool:write",
		EnvTag:        testEnvProd,
		HasTarget:     true,
	}
	v, err := Decide(ModeFull, Descriptor{Risk: RiskWrite, Confirm: ConfirmEnvTag}, callProd, tags, grants)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if v.Kind != VerdictAsk || !slices.Equal(v.Offers, []Outcome{OutcomeAllowedOnce}) {
		t.Fatalf("hard rule at Full mode = %+v, want Ask([allowed_once])", v)
	}

	vAlways, err := Decide(ModeFull, Descriptor{Risk: RiskRead, Confirm: ConfirmAlways}, Call{}, tags, grants)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if vAlways.Kind != VerdictAsk || !slices.Equal(vAlways.Offers, []Outcome{OutcomeAllowedOnce}) {
		t.Fatalf("ConfirmAlways at Full mode = %+v, want Ask([allowed_once])", vAlways)
	}

	vCrit, err := Decide(ModeFull, Descriptor{Risk: RiskRead, Confirm: ConfirmNever}, Call{Critical: true}, tags, grants)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if vCrit.Kind != VerdictAsk || !slices.Equal(vCrit.Offers, []Outcome{OutcomeAllowedOnce}) {
		t.Fatalf("Critical at Full mode = %+v, want Ask([allowed_once])", vCrit)
	}
}

func testIE02OwnerHardRuleBehavior(t *testing.T) {
	t.Helper()
	owner, _, _, _ := newTestOwner(t)
	spec := confirmSpec("op-ie02-hard-rule")
	spec.Binding.EnvTag = testEnvProd
	spec.Binding.HasTarget = true
	spec.Offers = []Outcome{OutcomeAllowedOnce}

	ticket, _, err := owner.Ensure(spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if slices.Contains(ticket.Offers, OutcomeAllowedSession) {
		t.Fatalf("hard rule ticket offered session grant: %+v", ticket.Offers)
	}

	_, err = owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedSession,
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument answering session grant for hard rule, got: %v", err)
	}

	answered, err := owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if err != nil {
		t.Fatalf("Answer once: %v", err)
	}
	if !answered.Resolved || answered.Outcome != OutcomeAllowedOnce {
		t.Fatalf("answered ticket = %+v, want resolved allowed_once", answered)
	}
	if owner.HasBindingGrant(&spec.Binding) {
		t.Fatalf("owner created a grant for hard rule execution")
	}
}

func testIE02PlanModeRefusals(t *testing.T, tags TagPolicy, grants GrantLookup) {
	t.Helper()
	vPlanRead, err := Decide(ModePlan, Descriptor{Risk: RiskRead, Confirm: ConfirmNever}, Call{}, tags, grants)
	if err != nil || vPlanRead.Kind != VerdictRun {
		t.Fatalf("Plan mode read = %+v, err=%v, want Run", vPlanRead, err)
	}

	for _, risk := range []Risk{RiskWrite, RiskDestructive, RiskExternal} {
		vPlanRefuse, err := Decide(ModePlan, Descriptor{Risk: risk, Confirm: ConfirmNever}, Call{}, tags, grants)
		if err != nil {
			t.Fatalf("Decide Plan mode %s: %v", risk, err)
		}
		if vPlanRefuse.Kind != VerdictRefuse || vPlanRefuse.Reason != RefusePlanMode {
			t.Fatalf("Plan mode %s = %+v, want Refuse(plan_mode)", risk, vPlanRefuse)
		}
	}
}

func TestIE02HardRuleAtFullAndPlanModeRefusal(t *testing.T) {
	t.Parallel()

	tags := newStubTagPolicy()
	grants := GrantLookupFunc(func(key string) bool { return true })

	testIE02HardRuleDecisions(t, tags, grants)
	testIE02OwnerHardRuleBehavior(t)
	testIE02PlanModeRefusals(t, tags, grants)
}

func testIE03ScopeKeyMatching(t *testing.T, owner *Owner, tags TagPolicy, spec *Spec, baseScopeKey string) {
	t.Helper()
	ticket, _, err := owner.Ensure(spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	_, err = owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedSession,
	})
	if err != nil {
		t.Fatalf("Answer session grant: %v", err)
	}

	if !owner.HasBindingGrant(&spec.Binding) {
		t.Fatalf("owner has no grant for base scope key")
	}

	offByOne := spec.Binding
	offByOne.GrantScopeKey = "edit:dir:fugue/src"
	if owner.HasBindingGrant(&offByOne) {
		t.Fatalf("grant matched key differing by 1 character")
	}

	vExact, err := Decide(
		ModeAsk,
		Descriptor{Risk: RiskWrite, Confirm: ConfirmNever},
		Call{GrantScopeKey: baseScopeKey},
		tags,
		owner.GrantLookupFor(&spec.Binding),
	)
	if err != nil {
		t.Fatalf("Decide exact: %v", err)
	}
	if vExact.Kind != VerdictRun || vExact.Outcome != OutcomeAllowedSession {
		t.Fatalf("exact scope key verdict = %+v, want Run(allowed_session)", vExact)
	}

	vOffByOne, err := Decide(
		ModeAsk,
		Descriptor{Risk: RiskWrite, Confirm: ConfirmNever},
		Call{GrantScopeKey: "edit:dir:fugue/src"},
		tags,
		owner.GrantLookupFor(&spec.Binding),
	)
	if err != nil {
		t.Fatalf("Decide off-by-one: %v", err)
	}
	if vOffByOne.Kind != VerdictAsk {
		t.Fatalf("off-by-one scope key verdict = %+v, want Ask", vOffByOne)
	}
}

func newOwnerWithTags(t *testing.T, tags TagPolicy) *Owner {
	t.Helper()
	owner, err := NewOwner(&Config{
		Clock:     clock.NewFake(startTime()),
		IDs:       ids.NewSequence("id"),
		Responder: NopResponder{},
		TagPolicy: tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

// testIE03RevisionInvalidation changes the tag policy without touching the
// binding: the Owner must derive the revision itself, so the old grant stops
// covering the call, also after the policy is reverted.
func testIE03RevisionInvalidation(t *testing.T, owner *Owner, tags *stubTagPolicy, spec *Spec) {
	t.Helper()
	if err := tags.set(testEnvDev, true); err != nil {
		t.Fatal(err)
	}
	if owner.HasBindingGrant(&spec.Binding) {
		t.Fatalf("grant remained valid after the tag policy changed")
	}
	if owner.GrantLookupFor(&spec.Binding).HasGrant(spec.Binding.GrantScopeKey) {
		t.Fatalf("GrantLookupFor still matched after the tag policy changed")
	}

	if err := tags.set(testEnvDev, false); err != nil {
		t.Fatal(err)
	}
	if owner.HasBindingGrant(&spec.Binding) {
		t.Fatalf("grant remained valid after the tag policy was reverted")
	}
}

func TestIE03GrantScopeKeyMatchingAndPolicyRevisionInvalidation(t *testing.T) {
	t.Parallel()

	tags := newStubTagPolicy()
	owner := newOwnerWithTags(t, tags)

	const baseScopeKey = "edit:dir:fugue/src/"
	spec := confirmSpec("op-ie03-grant")
	spec.Binding.GrantScopeKey = baseScopeKey
	spec.Binding.HasTarget = true
	spec.Binding.EnvTag = testEnvDev
	spec.Offers = []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession}

	testIE03ScopeKeyMatching(t, owner, tags, spec, baseScopeKey)
	testIE03RevisionInvalidation(t, owner, tags, spec)
}

func TestIE03NilTagPolicyNeverKeysTargetGrants(t *testing.T) {
	t.Parallel()

	owner := newOwnerWithTags(t, nil)
	b := defaultBinding()
	b.HasTarget = true
	if owner.HasBindingGrant(&b) || owner.GrantLookupFor(&b).HasGrant(b.GrantScopeKey) {
		t.Fatalf("target binding matched a grant with nil TagPolicy")
	}
}

func testIE10MutationProbes(t *testing.T, tags TagPolicy) {
	t.Helper()
	vStep1, err := Decide(
		ModeFull,
		Descriptor{Risk: RiskWrite, Confirm: ConfirmEnvTag},
		Call{HasTarget: true, EnvTag: testEnvProd},
		tags,
		nil,
	)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if vStep1.Kind != VerdictAsk || !slices.Equal(vStep1.Offers, []Outcome{OutcomeAllowedOnce}) {
		t.Fatalf("mutant survived: step 1 omitted, got %+v", vStep1)
	}

	grants := GrantLookupFunc(func(key string) bool {
		return key == "scope:A"
	})
	vKeyMismatch, err := Decide(
		ModeAsk,
		Descriptor{Risk: RiskWrite, Confirm: ConfirmNever},
		Call{GrantScopeKey: "scope:B"},
		tags,
		grants,
	)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if vKeyMismatch.Kind != VerdictAsk {
		t.Fatalf("mutant survived: key matching omitted, got %+v", vKeyMismatch)
	}

	vCustomDowngrade, err := Decide(
		ModeFull,
		Descriptor{Risk: RiskWrite, Confirm: ConfirmCustom},
		Call{
			HasTarget:        true,
			EnvTag:           testEnvProd,
			EffectiveRisk:    RiskWrite,
			EffectiveConfirm: ConfirmNever,
		},
		tags,
		nil,
	)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if vCustomDowngrade.Kind != VerdictAsk || !slices.Equal(vCustomDowngrade.Offers, []Outcome{OutcomeAllowedOnce}) {
		t.Fatalf("mutant survived: custom confirm downgraded below hard rule, got %+v", vCustomDowngrade)
	}
}

func testIE10ValidationProbes(t *testing.T, tags TagPolicy) {
	t.Helper()
	if _, err := Decide(ApprovalMode(testBogus), Descriptor{Risk: RiskRead, Confirm: ConfirmNever}, Call{}, tags, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("invalid mode error = %v, want InvalidArgument", err)
	}
	if _, err := Decide(ModeAuto, Descriptor{Risk: Risk(testBogus), Confirm: ConfirmNever}, Call{}, tags, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("invalid risk error = %v, want InvalidArgument", err)
	}
	if _, err := Decide(ModeAuto, Descriptor{Risk: RiskRead, Confirm: Confirm(testBogus)}, Call{}, tags, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("invalid confirm error = %v, want InvalidArgument", err)
	}
	if _, err := Decide(ModeAuto, Descriptor{Risk: RiskRead, Confirm: ConfirmCustom}, Call{EffectiveRisk: Risk(testBogus), EffectiveConfirm: ConfirmNever}, tags, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("invalid custom risk error = %v, want InvalidArgument", err)
	}
	if _, err := Decide(ModeAuto, Descriptor{Risk: RiskRead, Confirm: ConfirmCustom}, Call{EffectiveRisk: RiskRead, EffectiveConfirm: Confirm(testBogus)}, tags, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("invalid custom confirm error = %v, want InvalidArgument", err)
	}
}

func TestIE10DecideMutationProbes(t *testing.T) {
	t.Parallel()

	tags := newStubTagPolicy()
	testIE10MutationProbes(t, tags)
	testIE10ValidationProbes(t, tags)
}

func TestOwnerNilTagPolicyFailsClosed(t *testing.T) {
	t.Parallel()

	fakeClock := clock.NewFake(startTime())
	owner, err := NewOwner(&Config{
		Clock:     fakeClock,
		IDs:       ids.NewSequence("nil-tags"),
		TagPolicy: nil,
	})
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}

	// When HasTarget is true: nil TagPolicy treats target as requiring confirmation (fail closed)
	targetSpec := confirmSpec("op-target-nil-policy")
	targetSpec.Binding.HasTarget = true
	targetSpec.Binding.EnvTag = testEnvDev
	targetSpec.Binding.GrantScopeKey = "tool:write"
	targetSpec.Offers = []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession}

	_, _, err = owner.Ensure(targetSpec)
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument for HasTarget=true with nil TagPolicy, got: %v", err)
	}

	// When HasTarget is false: nil TagPolicy does NOT fail closed, session grant allowed
	noTargetSpec := confirmSpec("op-no-target-nil-policy")
	noTargetSpec.Binding.HasTarget = false
	noTargetSpec.Binding.EnvTag = ""
	noTargetSpec.Binding.GrantScopeKey = "tool:local"
	noTargetSpec.Offers = []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession}

	ticket, _, err := owner.Ensure(noTargetSpec)
	if err != nil {
		t.Fatalf("Ensure HasTarget=false with nil TagPolicy failed: %v", err)
	}
	if !slices.Contains(ticket.Offers, OutcomeAllowedSession) {
		t.Fatalf("offers = %v, want OutcomeAllowedSession offered", ticket.Offers)
	}
}

func TestLocalAndTestTagsRequireConfirmation(t *testing.T) {
	t.Parallel()

	owner, _, _, _ := newTestOwner(t)

	for _, tag := range []string{"local", "test"} {
		spec := confirmSpec("op-tag-" + tag)
		spec.Binding.SessionID = "sess-" + tag
		spec.Binding.HasTarget = true
		spec.Binding.EnvTag = tag
		spec.Binding.GrantScopeKey = "tool:" + tag
		spec.Offers = []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession}

		// Offering allowed_session for "local" or "test" target is rejected because they now require confirmation
		_, _, err := owner.Ensure(spec)
		if errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("tag %q with HasTarget=true should require confirmation, got err: %v", tag, err)
		}

		// Offering only allowed_once succeeds and asks
		spec.Offers = []Outcome{OutcomeAllowedOnce}
		ticket, _, err := owner.Ensure(spec)
		if err != nil {
			t.Fatalf("Ensure once for %q failed: %v", tag, err)
		}
		if ticket.Resolved || !slices.Equal(ticket.Offers, []Outcome{OutcomeAllowedOnce}) {
			t.Fatalf("ticket for %q = %+v, want pending Ask([allowed_once])", tag, ticket)
		}
	}
}

func TestEmptyGrantScopeKeyWithSessionGrantFails(t *testing.T) {
	t.Parallel()

	owner, _, _, _ := newTestOwner(t)

	spec := confirmSpec("op-empty-grant-scope")
	spec.Binding.GrantScopeKey = ""
	spec.Offers = []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession}

	_, _, err := owner.Ensure(spec)
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument for empty GrantScopeKey with allowed_session offer, got: %v", err)
	}
}

func TestVerdictHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		v          *Verdict
		wantRun    bool
		wantAsk    bool
		wantRefuse bool
	}{
		{"nil", nil, false, false, false},
		{"run", &Verdict{Kind: VerdictRun}, true, false, false},
		{"ask", &Verdict{Kind: VerdictAsk}, false, true, false},
		{"refuse", &Verdict{Kind: VerdictRefuse}, false, false, true},
	}
	for _, tc := range tests {
		if tc.v.IsRun() != tc.wantRun || tc.v.IsAsk() != tc.wantAsk || tc.v.IsRefuse() != tc.wantRefuse {
			t.Errorf("%s helpers failed", tc.name)
		}
	}
}
