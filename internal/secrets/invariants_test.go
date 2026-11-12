package secrets

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/redact"
)

const fixturePathGlob = "fixtures/*.txt"

func TestCSE01PriorityAndExceptions(t *testing.T) {
	t.Parallel()
	value := patternFixtures()[KindGitHubToken][0]
	text := "API_TOKEN=" + value
	known := []KnownValue{{Value: redact.NewSecret(value), Kind: KindVault}}
	for _, opts := range []Options{{}, {AllowKeys: []string{sampleKey}}, {AllowPaths: []string{fixturePathGlob}}} {
		got := New(known, opts).FindIn(text, "fixtures/test.txt")
		if len(got) != 1 || got[0].Kind != KindVault {
			t.Fatal("known priority or exception failed")
		}
	}
	if got := Find(text); len(got) != 1 || got[0].Kind != KindGitHubToken {
		t.Fatal("pattern priority failed")
	}
	if got := New(nil, Options{AllowKeys: []string{sampleKey}}).Find("API_TOKEN=abcd1234"); len(got) != 0 {
		t.Fatal("key exception failed")
	}
	if got := New(nil, Options{AllowKeys: []string{"OTHER"}}).Find("API_TOKEN=abcd1234"); len(got) != 1 {
		t.Fatal("key exception overmatched")
	}
}

func TestCSE01PathExceptions(t *testing.T) {
	t.Parallel()
	text := "API_TOKEN=" + patternFixtures()[KindGitHubToken][0]
	for _, glob := range []string{fixturePathGlob, "["} {
		d := New(nil, Options{AllowPaths: []string{glob}})
		count := len(d.FindIn(text+"\nPASSWORD=abcd1234", "fixtures/test.txt"))
		if glob == "[" && count != 2 || glob != "[" && count != 0 {
			t.Fatal("path exception failed")
		}
	}
}

func TestKnownValueFilteringAndRebuild(t *testing.T) {
	t.Parallel()
	for _, v := range []KnownValue{
		{Value: redact.NewSecret("abcd1234"), Kind: KindEnv, Key: "PATH"},
		{Value: redact.NewSecret("abcd123"), Kind: KindVault},
		{Value: redact.NewSecret("abcd1234"), Kind: "other"},
	} {
		if len(New([]KnownValue{v}, Options{}).Find(v.Value.Reveal())) != 0 {
			t.Fatal("filtered value matched")
		}
	}
	old := New([]KnownValue{{Value: redact.NewSecret("abcdefgh"), Kind: KindCredential}}, Options{})
	rebuilt := New([]KnownValue{{Value: redact.NewSecret("bcdefghi"), Kind: KindVault}}, Options{})
	if len(old.Find("abcdefgh")) != 1 || len(rebuilt.Find("abcdefgh")) != 0 || len(rebuilt.Find("bcdefghi")) != 1 {
		t.Fatal("rebuild mismatch")
	}
}

func TestKnownOverlaps(t *testing.T) {
	t.Parallel()
	values := []KnownValue{
		{Value: redact.NewSecret("abcdefgh"), Kind: KindVault},
		{Value: redact.NewSecret("bcdefghi"), Kind: KindEnv, Key: "TOKEN"},
		{Value: redact.NewSecret("cdefghij"), Kind: KindCredential},
	}
	got := New(values, Options{}).Find("abcdefghij")
	if diff := cmp.Diff([]Finding{{Start: 0, End: 10, Kind: KindVault}}, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestAssignmentNameDenylist(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"myToken", "mySecret", "myPassword", "dbPasswd", "myCredential", "myPrivate", "apiKey", "AWS_REGION", "APIKey", "awsRegion"} {
		if len(Find(key+"=abcd1234")) != 1 {
			t.Fatal("denylist mismatch")
		}
	}
	for _, line := range []string{"PASSWORD=12345678", "PASSWORD=éabc", "  export PASSWORD = 'abcd1234'", `{"PASSWORD": "abcd1234"}`} {
		if len(Find(line)) != 1 {
			t.Fatal("assignment boundary mismatch")
		}
	}
	for _, line := range []string{"=abcd1234", "1TOKEN=abcd1234", "PASSWORD='unterminated", `PASSWORD="abcd1234" + foo`, "PASSWORD", "PASSWORD=TRUE"} {
		if len(Find(line)) != 0 {
			t.Fatal("invalid assignment matched")
		}
	}
}

func TestMaskInvalidFindings(t *testing.T) {
	t.Parallel()
	got := Mask("abcdefgh", []Finding{{Start: -1, End: 2}, {Start: 2, End: 2}, {Start: 0, End: 9}, {Start: 0, End: 4, Kind: "untrusted"}, {Start: 1, End: 5}})
	if got != "[secret:unknown]efgh" {
		t.Fatal("invalid mask span handling")
	}
}

func mixedText(size int) string {
	line := "ordinary mixed text 1234\nAPI_TOKEN=abcd1234\n"
	return strings.Repeat(line, size/len(line)+1)[:size]
}

func TestFindOneMiBLinearBound(t *testing.T) {
	t.Parallel()
	d := New([]KnownValue{{Value: redact.NewSecret("abcd1234"), Kind: KindVault}}, Options{})
	measure := func(size int) ([]int64, testing.BenchmarkResult) {
		text := mixedText(size)
		samples := make([]int64, 0, 5)
		result := testing.Benchmark(func(b *testing.B) {
			b.Helper()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for len(samples) < 5 {
					start := b.Elapsed()
					d.Find(text)
					samples = append(samples, (b.Elapsed() - start).Nanoseconds())
				}
				d.Find(text)
			}
		})
		slices.Sort(samples)
		return samples, result
	}
	short, _ := measure(256 << 10)
	long, result := measure(1 << 20)
	enforceScanRatio(t, short[2], long[2])
	if result.AllocedBytesPerOp() > 256<<20 {
		t.Fatal("1 MiB scan exceeded allocation bound")
	}
	text := mixedText(1 << 20)
	got := d.Find(text)
	if len(got) == 0 || strings.Contains(Mask(text, got), "abcd1234") {
		t.Fatal("1 MiB masking failed")
	}
}

func BenchmarkFindOneMiB(b *testing.B) {
	d := New([]KnownValue{{Value: redact.NewSecret("abcd1234"), Kind: KindVault}}, Options{})
	text := mixedText(1 << 20)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d.Find(text)
	}
}

func FuzzFind(f *testing.F) {
	f.Add("plain text")
	f.Add("PASSWORD=abcd1234")
	f.Add("abcdefghij")
	f.Fuzz(func(t *testing.T, text string) {
		values := []KnownValue{{Value: redact.NewSecret("abcdefgh"), Kind: KindVault}, {Value: redact.NewSecret("bcdefghi"), Kind: KindCredential}}
		d := New(values, Options{})
		got := d.Find(text)
		end := 0
		for _, finding := range got {
			if finding.Start < end || finding.End <= finding.Start || finding.End > len(text) {
				t.Fatal("span invariant failed")
			}
			end = finding.End
		}
		masked := Mask(text, got)
		for _, value := range values {
			if strings.Contains(masked, value.Value.Reveal()) {
				t.Fatal("known value remained after masking")
			}
		}
	})
}

func TestAllowKeySuppressesPatternsInAssignmentOnly(t *testing.T) {
	t.Parallel()
	value := patternFixtures()[KindGitHubToken][0]
	d := New(nil, Options{AllowKeys: []string{sampleKey}})
	if len(d.Find("API_TOKEN="+value)) != 0 {
		t.Fatal("allowed key still matched a pattern")
	}
	if len(d.Find(value)) != 1 {
		t.Fatal("key exception suppressed unrelated token")
	}
}

func TestCSE01PartialSourceOverlaps(t *testing.T) {
	t.Parallel()
	d := New([]KnownValue{{Value: redact.NewSecret("abcdefgh"), Kind: KindVault}}, Options{})
	for _, text := range []string{"gh" + "p_abcdefgh" + strings.Repeat("x", 22) + "123456", "API_TOKEN=abcdefgh123456"} {
		got := d.Find(text)
		if len(got) != 1 || got[0].Kind != KindVault || strings.Contains(Mask(text, got), "123456") {
			t.Fatal("partial overlap left recognized material visible")
		}
	}
	begin, end := "-----BEGIN PRIVATE KEY-----\n", "\n-----END PRIVATE KEY-----"
	text := begin + "abcdefgh\nQUJDREVG" + end
	if Mask(text, d.Find(text)) != begin+"[secret:vault]"+end {
		t.Fatal("known overlap exposed PEM body")
	}
}

func TestCSE03LegacyEncryptedPEM(t *testing.T) {
	t.Parallel()
	begin, end := "-----BEGIN "+"RSA PRIVATE KEY-----\n", "\n-----END RSA PRIVATE KEY-----"
	body := "Proc-Type: 4,ENCRYPTED\nDEK-Info: AES-256-CBC," + strings.Repeat("AB", 16) + "\n\n" + strings.Repeat("QUJD", 20)
	if Mask(begin+body+end, Find(begin+body+end)) != begin+"[secret:private-key]"+end {
		t.Fatal("legacy PEM body not masked")
	}
	if len(Find(begin+body)) != 0 {
		t.Fatal("incomplete legacy PEM matched")
	}
}

func TestCSE01AssignmentTokenCharacters(t *testing.T) {
	t.Parallel()
	fixtures := map[string]string{
		"aws-secret-shaped": "wJal" + strings.Repeat("rXUtnFEMI/K7MDENG/", 2),
		"base64-padding":    "YWJj" + strings.Repeat("ZGVm", 4) + "==",
		"hyphenated-key":    "abcd-" + "efgh-1234",
		"url-safe-base64":   "YWJj_" + "ZGVm-abcd_1234",
		"token-punctuation": "abcd+efgh/1234==:xy.z",
	}
	for name, value := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			text := "API_KEY=" + value
			got := Find(text)
			if len(got) != 1 || got[0].Kind != KindAssigned || text[got[0].Start:got[0].End] != value {
				t.Fatal("token value was skipped")
			}
		})
	}
	for _, value := range []string{`os.Getenv("X")`, "a + b", "${TOKEN}"} {
		if len(Find("API_KEY="+value)) != 0 {
			t.Fatal("expression or reference detected as value")
		}
	}
}

func TestCSE01TokenMinimumLengths(t *testing.T) {
	t.Parallel()
	fixtures := map[string]struct{ kind, short string }{
		"github-classic": {KindGitHubToken, "gh" + "p_" + strings.Repeat("a", 35)},
		"github-pat":     {KindGitHubToken, "github_" + "pat_" + strings.Repeat("a", 21)},
		"google":         {KindGoogleAPIKey, "AI" + "za" + strings.Repeat("a", 34)},
		"openai":         {KindOpenAIToken, "s" + "k-" + strings.Repeat("a", 19)},
		"anthropic":      {KindAnthropic, "sk-" + "ant-" + strings.Repeat("a", 19)},
		"slack":          {KindSlackToken, "xo" + "xb-" + strings.Repeat("a", 9)},
		"stripe":         {KindStripeToken, "sk_" + "live_" + strings.Repeat("a", 15)},
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if len(Find(fixture.short)) != 0 {
				t.Fatal("undersized pattern matched")
			}
			got := Find(fixture.short + "a")
			if len(got) != 1 || got[0].Kind != fixture.kind {
				t.Fatal("minimum-size pattern not detected")
			}
		})
	}
	for _, value := range []string{"sk-1", "AIzaX", "ghp_a"} {
		if len(Find(value)) != 0 {
			t.Fatal("short prefix matched")
		}
	}
}

func TestCSE01PerCallPaths(t *testing.T) {
	t.Parallel()
	globs := []string{fixturePathGlob, ""}
	d := New([]KnownValue{{Value: redact.NewSecret("known-value-123"), Kind: KindVault}}, Options{AllowPaths: globs})
	globs[0] = "*"
	root := &d.nodes[0]
	text := "known-value-123\nAPI_KEY=abcd1234"
	for name, expected := range map[string]int{"fixtures/test.txt": 1, "src/test.go": 2, "": 2} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if len(d.FindIn(text, name)) != expected {
				t.Fatal("per-call path exception failed")
			}
			if &d.nodes[0] != root {
				t.Fatal("automaton was rebuilt during FindIn")
			}
		})
	}
	if len(d.Find(text)) != 2 {
		t.Fatal("Find must mean no path")
	}
}

func TestCSE01FirstAssignmentPerLine(t *testing.T) {
	t.Parallel()
	text := `{"API_KEY":"abcd1234","PASSWORD":"efgh5678"}`
	got := Find(text)
	if len(got) != 1 || got[0].Key != "API_KEY" {
		t.Fatal("first assignment limit changed")
	}
}

func TestCSE01GoogleSuffixBoundary(t *testing.T) {
	t.Parallel()
	value := "AI" + "za" + strings.Repeat("a", 34) + "-"
	for _, tail := range []string{"", "\"", " "} {
		got := Find(value + tail)
		if len(got) != 1 || got[0].Kind != KindGoogleAPIKey || got[0].End != len(value) {
			t.Fatal("Google suffix boundary failed")
		}
	}
	if len(Find(value+"a")) != 0 {
		t.Fatal("oversized Google body matched a partial key")
	}
}
