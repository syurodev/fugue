package secrets

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/redact"
)

const sampleKey = "apiToken"

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestCSE01KnownValues(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{KindEnv, KindCredential, KindVault} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			d := New([]KnownValue{{Value: redact.NewSecret("test-value-123"), Kind: kind, Key: sampleKey}}, Options{})
			if diff := cmp.Diff([]Finding{{Start: 2, End: 16, Kind: kind}}, d.Find("étest-value-123!")); diff != "" {
				t.Fatal(diff)
			}
			if len(d.Find("test-value-124")) != 0 {
				t.Fatal("negative matched")
			}
		})
	}
}

func TestCSE01Assignments(t *testing.T) {
	t.Parallel()
	for _, line := range []string{`apiToken=abcd1234`, `apiToken: abcd1234`, `"` + sampleKey + `": "abcd1234"`, `apiToken = "abcd1234"`, `export apiToken=abcd1234`} {
		got := Find(line)
		if len(got) != 1 || got[0].Kind != KindAssigned || got[0].Key != sampleKey || line[got[0].Start:got[0].End] != "abcd1234" {
			t.Fatal("assignment mismatch")
		}
	}
}

func TestCSE01AssignmentSkips(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "abc", `os.Getenv("X")`, "some value", "abcd+123", "abcd-123", "abcd(123)", "abcd[123]", "abcd{123}", "${VALUE}", "$VALUE", "{{VALUE}}", "<VALUE>", "xxxx", "****", "changeme", "example", "placeholder", "dummy", "true", "false", "null", "1234567"} {
		if len(Find("PASSWORD="+value)) != 0 {
			t.Fatal("skip rule failed")
		}
	}
	if len(Find("NORMAL=abcd1234")) != 0 {
		t.Fatal("ordinary key matched")
	}
}

func patternFixtures() map[string][]string {
	return map[string][]string{
		KindAWSAccessKey: {"AK" + "IA" + strings.Repeat("A1", 8), "AS" + "IA" + strings.Repeat("A1", 8)},
		KindGitHubToken:  {"gh" + "p_" + strings.Repeat("a1", 18), "gh" + "o_" + strings.Repeat("a1", 18), "gh" + "u_" + strings.Repeat("a1", 18), "gh" + "s_" + strings.Repeat("a1", 18), "gh" + "r_" + strings.Repeat("a1", 18), "github_" + "pat_" + strings.Repeat("a1", 41)},
		KindSlackToken:   {"xo" + "xb-1234567890-abcdef", "xo" + "xa-1234567890-abcdef", "xo" + "xp-1234567890-abcdef", "xo" + "xr-1234567890-abcdef", "xo" + "xs-1234567890-abcdef"},
		KindOpenAIToken:  {"s" + "k-" + strings.Repeat("a1", 24)},
		KindAnthropic:    {"sk-" + "ant-" + strings.Repeat("a1", 24)},
		KindGoogleAPIKey: {"AI" + "za" + strings.Repeat("a1", 17) + "a"},
		KindStripeToken:  {"sk_" + "live_" + strings.Repeat("a1", 12), "rk_" + "live_" + strings.Repeat("a1", 12)},
		KindJWT:          {"ey" + "JhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0.c2ln"},
	}
}

func TestCSE01TokenPatterns(t *testing.T) {
	t.Parallel()
	for kind, values := range patternFixtures() {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, value := range values {
				got := Find(value)
				if len(got) != 1 || got[0].Kind != kind || got[0].Start != 0 || got[0].End != len(value) {
					t.Fatal("pattern mismatch")
				}
				if len(Find("!"+value[1:])) != 0 {
					t.Fatal("negative pattern matched")
				}
			}
		})
	}
}

func TestCSE03PEMDelimiters(t *testing.T) {
	t.Parallel()
	begin, end := "-----BEGIN "+"RSA PRIVATE KEY-----\n", "\n-----END RSA PRIVATE KEY-----"
	body := strings.Repeat("QUJD", 20)
	text := begin + body + end
	if diff := cmp.Diff(begin+"[secret:private-key]"+end, Mask(text, Find(text))); diff != "" {
		t.Fatal("PEM delimiter mismatch")
	}
	if len(Find(begin+body)) != 0 {
		t.Fatal("incomplete PEM matched")
	}
}

func TestCSE03PEMVariants(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"PRIVATE KEY", "EC PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "OPENSSH PRIVATE KEY"} {
		for _, newline := range []string{"\n", "\r\n"} {
			begin := "-----BEGIN " + label + "-----" + newline
			end := newline + "-----END " + label + "-----"
			body := strings.Repeat("QUJD", 10) + newline + strings.Repeat("REVG", 10)
			if Mask(begin+body+end, Find(begin+body+end)) != begin+"[secret:private-key]"+end {
				t.Fatal("PEM variant mismatch")
			}
		}
	}
	if len(Find("-----BEGIN PRIVATE KEY-----\nQUJD\n-----END RSA PRIVATE KEY-----")) != 0 {
		t.Fatal("mismatched PEM labels matched")
	}
}
