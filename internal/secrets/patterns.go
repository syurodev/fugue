package secrets

import "regexp"

type tokenPattern struct {
	kind string
	re   *regexp.Regexp
}

// Compiled once and never mutated; regexp matching is safe concurrently.
var (
	awsAccessKeyPattern   = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	githubTokenPattern    = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+)\b`)
	slackTokenPattern     = regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]+`)
	anthropicTokenPattern = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]+`)
	openaiTokenPattern    = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+`)
	googleAPIKeyPattern   = regexp.MustCompile(`\bAIza[A-Za-z0-9_-]+`)
	stripeTokenPattern    = regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]+`)
	jwtPattern            = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	privateKeyPattern     = regexp.MustCompile(`(?m)^-----BEGIN ((?:[A-Z0-9]+ )*PRIVATE KEY)-----\r?\n((?:(?:[A-Za-z][A-Za-z0-9-]*: [^\r\n]*\r?\n)+\r?\n)?[A-Za-z0-9+/=]+(?:\r?\n[A-Za-z0-9+/=]+)*)\r?\n-----END ((?:[A-Z0-9]+ )*PRIVATE KEY)-----\r?$`)
)

func patterns() []tokenPattern {
	return []tokenPattern{
		{KindAWSAccessKey, awsAccessKeyPattern},
		{KindGitHubToken, githubTokenPattern},
		{KindSlackToken, slackTokenPattern},
		{KindAnthropic, anthropicTokenPattern},
		{KindOpenAIToken, openaiTokenPattern},
		{KindGoogleAPIKey, googleAPIKeyPattern},
		{KindStripeToken, stripeTokenPattern},
		{KindJWT, jwtPattern},
		{KindPrivateKey, privateKeyPattern},
	}
}

func findPatterns(text string, candidates []Finding) {
	for _, pattern := range patterns() {
		for _, match := range pattern.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := match[0], match[1]
			if pattern.kind == KindPrivateKey {
				if text[match[2]:match[3]] != text[match[6]:match[7]] {
					continue
				}
				start, end = match[4], match[5]
			}
			put(candidates, Finding{Start: start, End: end, Kind: pattern.kind})
		}
	}
}
