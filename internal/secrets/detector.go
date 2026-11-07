package secrets

import (
	"path"
	"slices"
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

// Options defines workspace exceptions. AllowPaths uses path.Match globs
// against slash-separated workspace-relative paths. Invalid globs never match.
// AllowKeys uses normalized key names. Exceptions never suppress known values.
type Options struct {
	AllowKeys  []string
	AllowPaths []string
}

// Detector is immutable after construction and safe for concurrent Find calls.
// Rebuild with New when values or options change; discard old instances when
// their owning vault/session closes. Its zero value detects patterns/assignments.
type Detector struct {
	nodes      []node
	allowKeys  map[string]bool
	allowPaths []string
}

// New builds an independent detector without reading the environment or vault.
// It retains only recognized kinds and values with at least eight characters.
func New(values []KnownValue, options Options) *Detector {
	d := &Detector{nodes: []node{{edges: make(map[byte]int)}}, allowKeys: make(map[string]bool)}
	for _, key := range options.AllowKeys {
		d.allowKeys[normalize(key)] = true
	}
	d.allowPaths = slices.Clone(options.AllowPaths)
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

// Find returns ordered, non-overlapping byte spans without a path exception.
// Known values win over patterns, which win over assignments. Overlapping
// matches are unioned under the highest-priority kind so no recognized secret
// is only partially masked. Equal-priority ties prefer earlier, longer spans.
func (d *Detector) Find(text string) []Finding { return d.FindIn(text, "") }

// FindIn applies path exceptions without rebuilding the known-value automaton.
// An empty filePath means no path; known values are never excepted.
func (d *Detector) FindIn(text, filePath string) []Finding {
	candidates := ranked(d.findKnown(text), 3)
	var allowed []Finding
	if !d.pathAllowed(filePath) {
		assignments, exceptions := d.findAssignments(text)
		allowed = exceptions
		candidates = append(candidates, ranked(findPatterns(text), 2)...)
		candidates = append(candidates, ranked(assignments, 1)...)
	}
	return selectCandidates(candidates, allowed)
}

func (d *Detector) pathAllowed(filePath string) bool {
	if filePath == "" {
		return false
	}
	for _, glob := range d.allowPaths {
		matched, err := path.Match(glob, filePath)
		if err == nil && matched {
			return true
		}
	}
	return false
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
