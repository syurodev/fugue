package app

// VR-E15 and HA-E13 evidence: SubmitLoginCode over a fake CLI reading from
// stdin, verifying accepted/missing-#/rejected flows, principal ownership,
// command_id dedupe, pre-stdin input validation, and secretlog hygiene.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
)

// codePromptCLI returns a fake CLI matching `claude auth login` behavior:
// prints the prompt without a trailing newline, reads lines from stdin,
// rejects codes without '#' on stderr and keeps waiting, rejects bad full
// codes on stderr and exits 1, and exits 0 for expectedCode.
func codePromptCLI(t *testing.T, expectedCode, stdinLogPath string) string {
	t.Helper()
	logLine := ""
	if stdinLogPath != "" {
		logLine = "printf '%s\\n' \"$code\" >> " + strconv.Quote(stdinLogPath) + "\n"
	}
	return writeLoginFakeCLI(t, `
printf 'Opening browser to sign in…\n'
printf 'If the browser didn'\''t open, visit:\n'
printf 'https://claude.ai/oauth/authorize?code=true\n'
printf 'Paste code here if prompted > '
while IFS= read -r code; do
  `+logLine+`case "$code" in
    *"#"*)
      if [ "$code" = `+strconv.Quote(expectedCode)+` ]; then
        exit 0
      fi
      printf '%s\n' "Couldn't log in: That code wasn't accepted." >&2
      exit 1
      ;;
    *)
      printf '%s\n' "That doesn't look like the full code. Copy all of it, including the # in the middle." >&2
      ;;
  esac
done
exit 1
`)
}

func startCodeLoginWithLogger(t *testing.T, expectedCode, stdinLogPath string, logger *slog.Logger) (*AccountsService, *fakeLoginAdapter, *Subscription, string) {
	t.Helper()
	f := newTranscriptFixture(t, nil)
	cli := codePromptCLI(t, expectedCode, stdinLogPath)
	ad := newFakeLoginAdapter(t, f.clock, cli, false)
	ad.acceptsCode = true
	svc, _, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad})
	svc.logger = logger
	sub := subscribeLoginTopic(t, f)
	view, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude)
	if err != nil {
		t.Fatal(err)
	}
	waitForLoginSpawn(t, ad)
	return svc, ad, sub, view.LoginID
}

func startCodeLogin(t *testing.T, expectedCode, stdinLogPath string) (*AccountsService, *fakeLoginAdapter, *Subscription, string) {
	t.Helper()
	return startCodeLoginWithLogger(t, expectedCode, stdinLogPath, nil)
}

func readLogInDir(t *testing.T, dir, name string) (string, error) {
	t.Helper()
	f, err := os.OpenInRoot(dir, name)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", name, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close %s: %v", name, cerr)
		}
	}()
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return string(raw), nil
}

func cancelCodeLoginCleanup(t *testing.T, svc *AccountsService, ad *fakeLoginAdapter) {
	t.Helper()
	if err := svc.CancelLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Errorf("CancelLogin cleanup: %v", err)
	}
	waitForProcessEnd(t, ad)
}

func TestVRE15LoginCodeAcceptedLogsIn(t *testing.T) {
	t.Parallel()
	testInput := strings.Join([]string{"valid-auth-input", "state-123"}, "#")
	svc, ad, sub, loginID := startCodeLogin(t, testInput, "")
	close(ad.release)

	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-1", loginID, redact.NewSecret(testInput)); err != nil {
		t.Fatalf("SubmitLoginCode: %v", err)
	}
	waitForProcessEnd(t, ad)
	drainUntilOutcome(t, sub, LoginSucceeded)
}

func TestVRE15LoginCodeMissingHashShowsStderrAndKeepsRunning(t *testing.T) {
	t.Parallel()
	testInput := strings.Join([]string{"valid-auth-input", "state-456"}, "#")
	svc, ad, sub, loginID := startCodeLogin(t, testInput, "")

	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-bad", loginID, redact.NewSecret("no-hash-here")); err != nil {
		t.Fatalf("SubmitLoginCode(no hash): %v", err)
	}
	for {
		p := nextLoginProgress(t, sub)
		if p.State != LoginWaiting && p.State != LoginStarted {
			t.Fatalf("unexpected terminal state %q while waiting for retry", p.State)
		}
		if strings.Contains(p.Prompt, "That doesn't look like the full code") {
			break
		}
	}

	close(ad.release)
	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-good", loginID, redact.NewSecret(testInput)); err != nil {
		t.Fatalf("SubmitLoginCode(retry): %v", err)
	}
	waitForProcessEnd(t, ad)
	drainUntilOutcome(t, sub, LoginSucceeded)
}

func TestVRE15LoginCodeRejectedFailsLogin(t *testing.T) {
	t.Parallel()
	svc, ad, sub, loginID := startCodeLogin(t, "expected#state", "")

	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-reject", loginID, redact.NewSecret("wrong#state")); err != nil {
		t.Fatalf("SubmitLoginCode: %v", err)
	}
	waitForProcessEnd(t, ad)
	for {
		p := nextLoginProgress(t, sub)
		if p.State == LoginFailed {
			if !strings.Contains(p.Prompt, "Couldn't log in: That code wasn't accepted.") {
				t.Fatalf("failed prompt = %q, want stderr rejection message", p.Prompt)
			}
			return
		}
	}
}

func TestHAE13SubmitLoginCodeAfterExitReturnsTypedError(t *testing.T) {
	t.Parallel()
	testInput := strings.Join([]string{"expected", "state"}, "#")
	svc, ad, sub, loginID := startCodeLogin(t, testInput, "")
	close(ad.release)
	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-1", loginID, redact.NewSecret(testInput)); err != nil {
		t.Fatalf("SubmitLoginCode: %v", err)
	}
	waitForProcessEnd(t, ad)
	drainUntilOutcome(t, sub, LoginSucceeded)

	err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-after", loginID, redact.NewSecret("late#state"))
	if errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("SubmitLoginCode after exit code = %v, want failed_precondition", errcode.CodeOf(err))
	}
}

func TestHAE13SubmitLoginCodeByAnotherPrincipalRefused(t *testing.T) {
	t.Parallel()
	svc, ad, _, loginID := startCodeLogin(t, "expected#state", "")
	defer cancelCodeLoginCleanup(t, svc, ad)

	for _, other := range []string{"other-app", PrincipalSession, "guard"} {
		err := svc.SubmitLoginCode(t.Context(), other, "cmd-"+other, loginID, redact.NewSecret("some#code"))
		if errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("SubmitLoginCode(%q) code = %v, want permission_denied", other, errcode.CodeOf(err))
		}
	}
}

func TestHAE13SubmitLoginCodeRepeatedCommandIDWritesOnce(t *testing.T) {
	t.Parallel()
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "stdin.log")
	const testInput = "missing-hash-once"
	svc, ad, _, loginID := startCodeLogin(t, "expected#state", logPath)
	defer cancelCodeLoginCleanup(t, svc, ad)

	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-dedup", loginID, redact.NewSecret(testInput)); err != nil {
		t.Fatalf("first SubmitLoginCode: %v", err)
	}
	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-dedup", loginID, redact.NewSecret(testInput)); err != nil {
		t.Fatalf("repeated SubmitLoginCode: %v", err)
	}
	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-dedup", "other-login-id", redact.NewSecret(testInput)); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("mismatched login_id with same command_id = %v, want invalid_argument", errcode.CodeOf(err))
	}
	testutil.Spin(t, "stdin log written once", func() bool {
		raw, err := readLogInDir(t, logDir, "stdin.log")
		if err != nil {
			return false
		}
		return strings.Count(raw, "\n") >= 1
	})
	raw, err := readLogInDir(t, logDir, "stdin.log")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(raw, "\n"); lines != 1 {
		t.Fatalf("stdin write count = %d, want 1", lines)
	}
}

func TestHAE13SubmitLoginCodeInvalidInputRefusedBeforeWrite(t *testing.T) {
	t.Parallel()
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "stdin.log")
	svc, ad, _, loginID := startCodeLogin(t, "expected#state", logPath)
	defer cancelCodeLoginCleanup(t, svc, ad)

	badInputs := []string{
		"",
		strings.Repeat("a", LoginCodeMaxBytes+1),
		"bad\ncode",
		"bad\rcode",
		"bad\x00code",
		"bad\x1bcode",
		"bad-non-ascii-é",
	}
	for i, raw := range badInputs {
		err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-bad-"+strconv.Itoa(i), loginID, redact.NewSecret(raw))
		if errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("case %d: code = %v, want invalid_argument", i, errcode.CodeOf(err))
		}
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stdin log exists after invalid inputs: %v", err)
	}
}

func TestHAE13SubmitLoginCodeAbsentFromLogsErrorsAndEvents(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	testInput := strings.Join([]string{"super-private-oauth-input", "state-999"}, "#")
	svc, ad, sub, loginID := startCodeLoginWithLogger(t, testInput, "", logger)
	close(ad.release)

	if err := svc.SubmitLoginCode(t.Context(), string(KindApp), "cmd-secret", loginID, redact.NewSecret(testInput)); err != nil {
		t.Fatalf("SubmitLoginCode: %v", err)
	}
	waitForProcessEnd(t, ad)
	for {
		p := nextLoginProgress(t, sub)
		if strings.Contains(p.Prompt, testInput) {
			t.Fatal("login progress prompt contains the submitted input")
		}
		if p.State == LoginSucceeded {
			break
		}
	}
	if strings.Contains(logBuf.String(), testInput) {
		t.Fatal("logs contain the submitted input")
	}
}
