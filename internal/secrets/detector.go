package secrets

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/redact"
)

// Detection kinds are stable marker names and contain no secret material.
const (
	KindEnv          = "env"
	KindCredential   = "credential"
	KindVault        = "vault"
	KindAssigned     = "assigned"
	KindAWSAccessKey = "aws-access-key"
	KindGitHubToken  = "github-token"
	KindSlackToken   = "slack-token"
	KindOpenAIToken  = "openai-token"
	KindAnthropic    = "anthropic-token"
	KindGoogleAPIKey = "google-api-key"
	KindStripeToken  = "stripe-token"
	KindJWT          = "jwt"
	KindPrivateKey   = "private-key"
)

// Finding identifies a half-open byte span; Key is set only for assignments.
// It never retains the matched value.
type Finding struct {
	Start int
	End   int
	Kind  string
	Key   string
}

// KnownValue supplies a protected value. Kind is env, credential or vault.
// Env values are included only when Key matches the J-04 denylist.
type KnownValue struct {
	Value redact.Secret
	Kind  string
	Key   string
}

// Options defines workspace exceptions. Path uses slash-separated workspace
// relative paths. AllowPaths uses path.Match globs. Invalid globs never match.
// AllowKeys uses normalized key names. Exceptions never suppress known values.
type Options struct {
	AllowKeys  []string
	AllowPaths []string
	Path       string
}

// Detector is immutable after construction and safe for concurrent Find calls.
// Rebuild with New when values or options change; discard old instances when
// their owning vault/session closes. Its zero value detects patterns/assignments.
type Detector struct {
	nodes     []node
	allowKeys map[string]bool
	allowPath bool
}

// New builds an independent detector without reading the environment or vault.
// It retains only recognized kinds and values with at least eight characters.
func New(values []KnownValue, options Options) *Detector {
	d := &Detector{nodes: []node{{edges: make(map[byte]int)}}, allowKeys: make(map[string]bool)}
	for _, key := range options.AllowKeys {
		d.allowKeys[normalize(key)] = true
	}
	for _, glob := range options.AllowPaths {
		matched, err := path.Match(glob, options.Path)
		if err == nil && matched {
			d.allowPath = true
		}
	}
	for _, v := range values {
		if validKnown(v) {
			d.insert(v.Value.Reveal(), v.Kind)
		}
	}
	d.buildFailures()
	return d
}

func validKnown(v KnownValue) bool {
	if utf8.RuneCountInString(v.Value.Reveal()) < 8 {
		return false
	}
	switch v.Kind {
	case KindEnv:
		return sensitiveName(v.Key)
	case KindCredential, KindVault:
		return true
	default:
		return false
	}
}

// Find detects patterns and assignments without a known-value set.
func Find(text string) []Finding { return (&Detector{}).Find(text) }

// Find returns ordered, non-overlapping byte spans. Known values win over
// patterns, which win over assignments. Equal starts prefer longer spans;
// overlapping matches are merged under the highest-priority kind so no
// recognized secret is only partially masked.
func (d *Detector) Find(text string) []Finding {
	selected := make([]Finding, len(text))
	d.findKnown(text, selected)
	if !d.allowPath {
		candidates := make([]Finding, len(text))
		d.findAssignments(text, candidates, true)
		blocked := coverage(candidates)
		ranks := make([]uint8, len(text))
		for i, f := range selected {
			if f.End > f.Start {
				ranks[i] = 3
			}
		}
		clear(candidates)
		findPatterns(text, candidates)
		selectCandidates(selected, candidates, blocked, ranks, 2)
		clear(candidates)
		d.findAssignments(text, candidates, false)
		selectCandidates(selected, candidates, blocked, ranks, 1)
	}
	var findings []Finding
	for _, f := range selected {
		if f.End > f.Start {
			findings = append(findings, f)
		}
	}
	return findings
}

func put(candidates []Finding, f Finding) {
	if f.End > f.Start && f.End > candidates[f.Start].End {
		candidates[f.Start] = f
	}
}

func coverage(selected []Finding) []int {
	prefix := make([]int, len(selected)+1)
	end := 0
	for i, f := range selected {
		end = max(end, f.End)
		prefix[i+1] = prefix[i]
		if i < end {
			prefix[i+1]++
		}
	}
	return prefix
}

// Each pass unions intervals in byte order. Ranks survive both passes, so
// a lower-priority interval bridging two findings cannot change their owner.
func selectCandidates(selected, candidates []Finding, blocked []int, ranks []uint8, rank uint8) {
	var current Finding
	var best uint8
	for i, existing := range selected {
		selected[i] = Finding{}
		if current.End > current.Start && i >= current.End {
			selected[current.Start], ranks[current.Start] = current, best
			current, best = Finding{}, 0
		}
		current, best = extend(current, best, existing, ranks[i])
		f := candidates[i]
		if f.End > f.Start && blocked[f.End] == blocked[f.Start] {
			current, best = extend(current, best, f, rank)
		}
	}
	if current.End > current.Start {
		selected[current.Start], ranks[current.Start] = current, best
	}
}

func extend(current Finding, best uint8, next Finding, rank uint8) (Finding, uint8) {
	if next.End <= next.Start {
		return current, best
	}
	if current.End <= current.Start {
		return next, rank
	}
	current.End = max(current.End, next.End)
	if rank > best {
		current.Kind, current.Key, best = next.Kind, next.Key, rank
	}
	return current, best
}

// Mask replaces valid, ordered spans with fixed kind markers. Invalid spans
// are ignored; callers should pass findings for this exact text from Find.
func Mask(text string, findings []Finding) string {
	var out strings.Builder
	end := 0
	for _, f := range findings {
		if f.Start < end || f.Start < 0 || f.End <= f.Start || f.End > len(text) {
			continue
		}
		out.WriteString(text[end:f.Start])
		out.WriteString("[secret:")
		out.WriteString(markerKind(f.Kind))
		out.WriteByte(']')
		end = f.End
	}
	out.WriteString(text[end:])
	return out.String()
}

func markerKind(kind string) string {
	switch kind {
	case KindEnv, KindCredential, KindVault, KindAssigned, KindAWSAccessKey, KindGitHubToken, KindSlackToken, KindOpenAIToken, KindAnthropic, KindGoogleAPIKey, KindStripeToken, KindJWT, KindPrivateKey:
		return kind
	default:
		return "unknown"
	}
}
