package redact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/redact"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const value = "sk-live-0123456789"

// render and logWith pass their arguments on through a wrapper on purpose: these
// tests must hand a Secret to a formatter and a logger, which the secretlog
// analyzer rightly forbids at a direct call site.
func render(format string, args ...any) string { return fmt.Sprintf(format, args...) }

func logWith(l *slog.Logger, args ...any) { l.Info("login", args...) }

func TestSecretNeverPrintsItsValue(t *testing.T) {
	t.Parallel()
	s := redact.NewSecret(value)
	outputs := map[string]string{
		"%s":     render("%s", s),
		"%v":     render("%v", s),
		"%+v":    render("%+v", s),
		"%#v":    render("%#v", s),
		"%q":     render("%q", s),
		"%x":     render("%x", s),
		"%d":     render("%d", s),
		"Sprint": render("%v", s),
		"ptr":    render("%v", &s),
		"struct": render("%+v", struct{ Token redact.Secret }{s}),
	}
	for verb, out := range outputs {
		if strings.Contains(out, value) {
			t.Errorf("%s leaked the value: %q", verb, out)
		}
	}
	if outputs["%s"] != redact.Mask {
		t.Errorf("%%s = %q, want %q", outputs["%s"], redact.Mask)
	}
}

func TestSecretIsMaskedInJSONAndSlog(t *testing.T) {
	t.Parallel()
	s := redact.NewSecret(value)
	data, err := json.Marshal(map[string]any{"secret": s, "list": []redact.Secret{s}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), value) {
		t.Errorf("JSON leaked the value: %s", data)
	}
	var buf bytes.Buffer
	logWith(slog.New(slog.NewTextHandler(&buf, nil)), "who", s)
	if strings.Contains(buf.String(), value) || !strings.Contains(buf.String(), redact.Mask) {
		t.Errorf("slog output = %q", buf.String())
	}
}

func TestRevealAndZero(t *testing.T) {
	t.Parallel()
	s := redact.NewSecret(value)
	if s.Reveal() != value || s.IsZero() {
		t.Errorf("Reveal = %q, IsZero = %v", s.Reveal(), s.IsZero())
	}
	if !(redact.Secret{}).IsZero() {
		t.Error("the zero Secret must be empty")
	}
	if s.Redacted() != redact.Mask || redact.String(value) != redact.Mask || redact.Bytes([]byte(value)) != redact.Mask {
		t.Error("explicit redaction helpers must return the mask")
	}
}

func object(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v is not an object", v)
	}
	return m
}

func TestHandlerMasksSecretLookingKeys(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(redact.NewJSONHandler(&buf, slog.LevelInfo))
	logger = logger.With("api_token", value).WithGroup("auth")
	logger.Info("login", "password", value, "user", "ana", slog.Group("nested", slog.String("Credential", value), slog.Int("n", 3)))

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if strings.Contains(buf.String(), value) {
		t.Fatalf("the handler leaked the value: %s", buf.String())
	}
	auth := object(t, got["auth"])
	nested := object(t, auth["nested"])
	for name, v := range map[string]any{"api_token": got["api_token"], "password": auth["password"], "Credential": nested["Credential"]} {
		if v != redact.Mask {
			t.Errorf("%s not masked: %v", name, v)
		}
	}
	if auth["user"] != "ana" || nested["n"] != float64(3) {
		t.Errorf("harmless attributes changed: %v %v", auth["user"], nested["n"])
	}
}

func TestTextHandlerMasksToo(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	slog.New(redact.NewTextHandler(&buf, slog.LevelInfo)).Info("x", "secret", value, "ok", 1)
	if strings.Contains(buf.String(), value) || !strings.Contains(buf.String(), "ok=1") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestOptionsSetTheLevel(t *testing.T) {
	t.Parallel()
	h := slog.NewTextHandler(&bytes.Buffer{}, redact.Options(slog.LevelWarn))
	if h.Enabled(t.Context(), slog.LevelInfo) || !h.Enabled(t.Context(), slog.LevelError) {
		t.Fatal("Options must carry the level")
	}
}

func TestSecretKey(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"token": true, "Authorization_Token": true, "password": true, "client_secret": true,
		"credentials": true, "privateKey": true, "ApiKeyValue": true, "user": false, "session_id": false,
	}
	for key, want := range cases {
		if got := redact.SecretKey(key); got != want {
			t.Errorf("SecretKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// brokenState is a fmt.State whose output always fails.
type brokenState struct{ fmt.State }

func (brokenState) Write([]byte) (int, error) { return 0, errBroken }

var errBroken = errors.New("broken pipe")

func TestFormatSurvivesAWriteFailure(t *testing.T) {
	t.Parallel()
	redact.NewSecret(value).Format(brokenState{}, 'v')
}

// fmt prefers Format, so the other interfaces are exercised directly: encoders
// and reflection-based printers reach them without going through fmt.
func TestEveryMarshalingInterfaceMasks(t *testing.T) {
	t.Parallel()
	s := redact.NewSecret(value)
	text, err := s.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"String": s.String(), "GoString": s.GoString(), "MarshalText": string(text)} {
		if strings.Contains(got, value) || !strings.Contains(got, redact.Mask) {
			t.Errorf("%s = %q", name, got)
		}
	}
}
