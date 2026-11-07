package secrets

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/redact"
)

func TestCSE01PriorityAndExceptions(t *testing.T) {
	t.Parallel()
	value := patternFixtures()[KindGitHubToken][0]
	text := "API_TOKEN=" + value
	known := []KnownValue{{Value: redact.NewSecret(value), Kind: KindVault}}
	for _, opts := range []Options{{}, {AllowKeys: []string{sampleKey}}, {AllowPaths: []string{"fixtures/*.txt"}, Path: "fixtures/test.txt"}} {
		got := New(known, opts).Find(text)
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
	for _, glob := range []string{"fixtures/*.txt", "["} {
		d := New(nil, Options{AllowPaths: []string{glob}, Path: "fixtures/test.txt"})
		count := len(d.Find(text + "\nPASSWORD=abcd1234"))
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
	for _, line := range []string{"=abcd1234", "1TOKEN=abcd1234", "PASSWORD='unterminated", `PASSWORD="abcd1234" + foo`, "PASSWORD", "PASSWORD=abcd/123", "PASSWORD=TRUE"} {
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
	text := mixedText(1 << 20)
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for b.Loop() {
			d.Find(text)
		}
	})
	if result.NsPerOp() > 30_000_000_000 || result.AllocedBytesPerOp() > 256<<20 {
		t.Fatal("1 MiB scan exceeded generous time/allocation bound")
	}
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
	for _, text := range []string{"gh" + "p_abcdefgh123456", "API_TOKEN=abcdefgh123456"} {
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
