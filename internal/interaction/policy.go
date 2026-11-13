package interaction

import (
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ApprovalMode is the session-level approval mode (interaction §2.2).
type ApprovalMode string

// The approval modes supported for Fugue tools (interaction §2.2).
const (
	ModePlan ApprovalMode = "plan"
	ModeAsk  ApprovalMode = "ask"
	ModeAuto ApprovalMode = "auto"
	ModeFull ApprovalMode = "full"
)

// Risk is the highest impact an operation can cause (interaction §3.1).
type Risk string

// The supported risk levels (interaction §3.1).
const (
	RiskRead        Risk = "read"
	RiskWrite       Risk = "write"
	RiskDestructive Risk = "destructive"
	RiskExternal    Risk = "external"
)

// Confirm specifies how confirmation is determined for a tool (interaction §3.1).
type Confirm string

// The supported confirmation policies (interaction §3.1).
const (
	ConfirmNever  Confirm = "never"
	ConfirmEnvTag Confirm = "env_tag"
	ConfirmAlways Confirm = "always"
	ConfirmCustom Confirm = "custom"
)

// Descriptor describes the static risk and confirmation policy of a tool (interaction §3.1).
type Descriptor struct {
	Risk    Risk
	Confirm Confirm
}

// Call describes the invocation-specific parameters evaluated by Decide (interaction §3.1, §3.2).
type Call struct {
	// GrantScopeKey is the canonical key of the scope covered by a session grant.
	GrantScopeKey string
	// EnvTag is the environment tag associated with the target (e.g. dev, prod).
	EnvTag string
	// EffectiveRisk is the effective risk level when descriptor.Confirm is ConfirmCustom.
	EffectiveRisk Risk
	// EffectiveConfirm is the effective confirmation policy when descriptor.Confirm is ConfirmCustom.
	EffectiveConfirm Confirm
	// HasTarget indicates whether the call operates against an execution target.
	HasTarget bool
	// Critical marks important operations of the tool system (tool system §5.3).
	Critical bool
	// NeedsEscalation marks whether the call requires permissions beyond the current
	// sandbox profile (interaction §3.2 step 3).
	NeedsEscalation bool
	// ShellAsk indicates whether static shell rule inspection concluded the command requires confirmation (§3.3).
	ShellAsk bool
}

// TagPolicy reports whether an environment tag requires confirmation and its
// monotonic policy revision counter (interaction §3.4).
type TagPolicy interface {
	RequiresConfirmation(name string) bool
	PolicyRevision(name string) uint64
}

// GrantLookup tests whether an active session grant covers a given grant scope key.
type GrantLookup interface {
	HasGrant(grantScopeKey string) bool
}

// GrantLookupFunc is an adapter allowing a plain function to act as a GrantLookup.
type GrantLookupFunc func(grantScopeKey string) bool

// HasGrant calls f(grantScopeKey).
func (f GrantLookupFunc) HasGrant(grantScopeKey string) bool {
	return f(grantScopeKey)
}

// VerdictKind distinguishes the outcome of a Decide evaluation (interaction §3.2).
type VerdictKind string

// Verdict kinds returned by Decide.
const (
	VerdictRun    VerdictKind = "run"
	VerdictAsk    VerdictKind = "ask"
	VerdictRefuse VerdictKind = "refuse"
)

// Refusal reason constants (interaction §3.2).
const (
	RefusePlanMode = "plan_mode"
)

// Verdict is the outcome of policy evaluation for a tool call (interaction §3.2).
type Verdict struct {
	Kind       VerdictKind
	Offers     []Outcome
	Reason     string
	Outcome    Outcome
	Escalation bool
}

// IsRun reports whether the verdict allows execution.
func (v *Verdict) IsRun() bool { return v != nil && v.Kind == VerdictRun }

// IsAsk reports whether the verdict requires prompting the user.
func (v *Verdict) IsAsk() bool { return v != nil && v.Kind == VerdictAsk }

// IsRefuse reports whether the verdict refuses execution without prompting.
func (v *Verdict) IsRefuse() bool { return v != nil && v.Kind == VerdictRefuse }

func validateDecideInputs(mode ApprovalMode, descriptor Descriptor, call Call) (Risk, Confirm, error) {
	switch mode {
	case ModePlan, ModeAsk, ModeAuto, ModeFull:
	default:
		return "", "", errcode.New(errcode.InvalidArgument, "invalid approval mode: "+string(mode))
	}

	switch descriptor.Risk {
	case RiskRead, RiskWrite, RiskDestructive, RiskExternal:
	default:
		return "", "", errcode.New(errcode.InvalidArgument, "invalid tool risk: "+string(descriptor.Risk))
	}

	switch descriptor.Confirm {
	case ConfirmNever, ConfirmEnvTag, ConfirmAlways, ConfirmCustom:
	default:
		return "", "", errcode.New(errcode.InvalidArgument, "invalid tool confirm: "+string(descriptor.Confirm))
	}

	if descriptor.Confirm != ConfirmCustom {
		return descriptor.Risk, descriptor.Confirm, nil
	}

	switch call.EffectiveRisk {
	case RiskRead, RiskWrite, RiskDestructive, RiskExternal:
	default:
		return "", "", errcode.New(errcode.InvalidArgument, "invalid effective risk for custom confirm: "+string(call.EffectiveRisk))
	}
	switch call.EffectiveConfirm {
	case ConfirmNever, ConfirmEnvTag, ConfirmAlways, ConfirmCustom:
	default:
		return "", "", errcode.New(errcode.InvalidArgument, "invalid effective confirm for custom confirm: "+string(call.EffectiveConfirm))
	}
	return call.EffectiveRisk, call.EffectiveConfirm, nil
}

func isHardRule(descriptor Descriptor, call Call, effectiveRisk Risk, effectiveConfirm Confirm, tags TagPolicy) bool {
	if call.Critical || effectiveConfirm == ConfirmAlways {
		return true
	}
	if !call.HasTarget || effectiveRisk == RiskRead {
		return false
	}
	if effectiveConfirm != ConfirmEnvTag && effectiveConfirm != ConfirmCustom && descriptor.Confirm != ConfirmCustom {
		return false
	}
	if tags == nil {
		return true
	}
	return tags.RequiresConfirmation(call.EnvTag)
}

func askOrSessionGrant(hasGrant, escalation bool) Verdict {
	if hasGrant {
		return Verdict{
			Kind:    VerdictRun,
			Outcome: OutcomeAllowedSession,
		}
	}
	return Verdict{
		Kind:       VerdictAsk,
		Offers:     []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession},
		Escalation: escalation,
	}
}

func isPlanRefusal(mode ApprovalMode, risk Risk) bool {
	return mode == ModePlan && risk != RiskRead
}

func checkGrant(key string, grants GrantLookup) bool {
	if key == "" || grants == nil {
		return false
	}
	return grants.HasGrant(key)
}

func autoNeedsConfirm(risk Risk, shellAsk bool) bool {
	return risk == RiskDestructive || risk == RiskExternal || shellAsk
}

// Decide evaluates approval policy for a tool call according to the seven steps
// of interaction §3.2. Evaluation stops at the first matching rule.
func Decide(
	mode ApprovalMode,
	descriptor Descriptor,
	call Call,
	tags TagPolicy,
	grants GrantLookup,
) (Verdict, error) {
	effectiveRisk, effectiveConfirm, err := validateDecideInputs(mode, descriptor, call)
	if err != nil {
		return Verdict{}, err
	}

	// Step 1: Hard rule I-03.
	if isHardRule(descriptor, call, effectiveRisk, effectiveConfirm, tags) {
		return Verdict{
			Kind:   VerdictAsk,
			Offers: []Outcome{OutcomeAllowedOnce},
		}, nil
	}

	// Step 2: Mode Plan and risk != read -> refuse(plan_mode).
	if isPlanRefusal(mode, effectiveRisk) {
		return Verdict{
			Kind:   VerdictRefuse,
			Reason: RefusePlanMode,
		}, nil
	}

	hasGrant := checkGrant(call.GrantScopeKey, grants)

	// Step 3: Call requires permissions beyond the current sandbox profile (escalation).
	if call.NeedsEscalation {
		return askOrSessionGrant(hasGrant, true), nil
	}

	// Step 4: Mode Full -> run.
	if mode == ModeFull {
		return Verdict{Kind: VerdictRun}, nil
	}

	// Step 5: Mode Auto: risk in {destructive, external} or shell rules ask.
	if mode == ModeAuto && autoNeedsConfirm(effectiveRisk, call.ShellAsk) {
		return askOrSessionGrant(hasGrant, false), nil
	}

	// Step 6: Mode Ask: risk != read.
	if mode == ModeAsk && effectiveRisk != RiskRead {
		return askOrSessionGrant(hasGrant, false), nil
	}

	// Step 7: Remainder -> run.
	return Verdict{Kind: VerdictRun}, nil
}

// HasBindingGrant reports whether a session grant exists matching the exact binding
// context and its GrantScopeKey. The environment revision is derived from the
// Owner's tag policy, not from the binding.
func (o *Owner) HasBindingGrant(b *Binding) bool {
	if b == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hasGrantLocked(b)
}

func (o *Owner) hasGrantLocked(b *Binding) bool {
	key, ok := o.grantKeyFor(b)
	if !ok {
		return false
	}
	_, found := o.grants[key]
	return found
}

// GrantLookupFor returns a GrantLookup implementation checking grants recorded
// in this Owner for the given execution context binding.
func (o *Owner) GrantLookupFor(b *Binding) GrantLookup {
	return GrantLookupFunc(func(key string) bool {
		if b == nil || key == "" {
			return false
		}
		cp := *b
		cp.GrantScopeKey = key
		return o.HasBindingGrant(&cp)
	})
}
