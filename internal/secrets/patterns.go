package secrets

import (
	"regexp"
	"strings"
)

type tokenPattern struct {
	kind    string
	re      *regexp.Regexp
	needles []string
}

// Compiled once and never mutated; regexp matching is safe concurrently.
var (
	// AWS access IDs have exactly sixteen uppercase alphanumeric suffix bytes.
	awsAccessKeyPattern = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	// GitHub classic suffixes are 36 bytes; fine-grained PATs need at least 22.
	githubTokenPattern = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})\b`)
	// Slack bodies need at least ten bytes after the xox?- prefix.
	slackTokenPattern = regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)
	// Anthropic bodies need at least twenty bytes after sk-ant-.
	anthropicTokenPattern = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)
	// OpenAI bodies need at least twenty bytes after sk-; sk-ant- is reserved below.
	openaiTokenPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)
	// Google API keys have exactly 35 bytes after AIza.
	googleAPIKeyPattern = regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}`)
	// Stripe live-key bodies need at least sixteen bytes after sk_live_/rk_live_.
	stripeTokenPattern = regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{16,}`)
	jwtPattern         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	privateKeyPattern  = regexp.MustCompile(`(?m)^-----BEGIN ((?:[A-Z0-9]+ )*PRIVATE KEY)-----\r?\n((?:(?:[A-Za-z][A-Za-z0-9-]*: [^\r\n]*\r?\n)+\r?\n)?[A-Za-z0-9+/=]+(?:\r?\n[A-Za-z0-9+/=]+)*)\r?\n-----END ((?:[A-Z0-9]+ )*PRIVATE KEY)-----\r?$`)
)

func patterns() []tokenPattern {
	return []tokenPattern{
		{KindAWSAccessKey, awsAccessKeyPattern, []string{"AKIA", "ASIA"}},
		{KindGitHubToken, githubTokenPattern, []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}},
		{KindSlackToken, slackTokenPattern, []string{"xox"}},
		{KindAnthropic, anthropicTokenPattern, []string{"sk-ant-"}},
		{KindOpenAIToken, openaiTokenPattern, []string{"sk-"}},
		{KindGoogleAPIKey, googleAPIKeyPattern, []string{"AIza"}},
		{KindStripeToken, stripeTokenPattern, []string{"k_live_"}},
		{KindJWT, jwtPattern, []string{"eyJ"}},
		{KindPrivateKey, privateKeyPattern, []string{"-----BEGIN "}},
	}
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func findPatterns(text string) []Finding {
	var findings []Finding
	for _, pattern := range patterns() {
		if !containsAny(text, pattern.needles) {
			continue
		}
		if pattern.kind == KindPrivateKey {
			findings = appendPrivateKeyFindings(findings, text, pattern.re)
			continue
		}
		findings = appendTokenFindings(findings, text, pattern.kind, pattern.re)
	}
	return findings
}

func appendPrivateKeyFindings(dst []Finding, text string, re *regexp.Regexp) []Finding {
	for _, match := range re.FindAllStringSubmatchIndex(text, -1) {
		if text[match[2]:match[3]] != text[match[6]:match[7]] {
			continue
		}
		dst = append(dst, Finding{Start: match[4], End: match[5], Kind: KindPrivateKey})
	}
	return dst
}

func appendTokenFindings(dst []Finding, text, kind string, re *regexp.Regexp) []Finding {
	for _, match := range re.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		if excludedPattern(kind, text, start, end) {
			continue
		}
		dst = append(dst, Finding{Start: start, End: end, Kind: kind})
	}
	return dst
}

// Google bodies may end in '-', so a word boundary would reject valid keys.
// Check the actual alphabet instead, and avoid masking a partial longer body.
func excludedPattern(kind, text string, start, end int) bool {
	switch kind {
	case KindOpenAIToken:
		return strings.HasPrefix(text[start:end], "sk-ant-")
	case KindGoogleAPIKey:
		return end < len(text) && strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-", rune(text[end]))
	default:
		return false
	}
}
