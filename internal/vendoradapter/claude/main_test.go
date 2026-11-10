package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

// The fake claude's sentinels: a CLAUDE_CONFIG_DIR that makes the version
// command exit non-zero, hang forever, or flood stdout. fixtureCLI is the
// pinned version the fixtures were recorded from.
const (
	fakeFailConfigDir               = "/probe/fail"
	fakeHangConfigDir               = "/probe/hang"
	fakeFloodConfigDir              = "/probe/flood"
	fakeNoInitConfigDir             = "/session/no-init"
	fakeIgnoreInterruptConfigDir    = "/session/ignore-interrupt"
	fakeFrameTooLargeConfigDir      = "/session/frame-too-large"
	fakeCheckEnvConfigDir           = "/session/check-env"
	fakePrintArgvConfigDir          = "/session/print-argv"
	fakeUntestedVersionConfigDir    = "/session/untested-version"
	fakeNeverReadStdinConfigDir     = "/session/never-read-stdin"
	fakeContinuousOutputConfigDir   = "/session/continuous-output"
	fakeCrashConfigDir              = "/session/crash"
	fakeReplaySteerFixtureConfigDir = "/session/replay-steer-fixture"
	fixtureCLI                      = "2.1.319"
)

// writeOut writes the fake CLI's stdout, exiting on failure like a real CLI
// would on a broken pipe.
func writeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		runFakeClaude()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "-p" {
		runFakeClaudeSession()
		os.Exit(0)
	}
	// The golden rewrite flag is registered here, not as a package variable,
	// so the package owns no mutable globals; the golden test reads it back
	// through flag.Lookup.
	flag.Bool("update", false, "rewrite the golden transcript files")
	goleak.VerifyTestMain(m)
}

// goldensRequested reports whether -update was passed.
func goldensRequested() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// runFakeClaude answers `claude --version` with the tested version string,
// after checking its own environment the way a leak would surface it: a
// forbidden variable or a non-dumb TERM yields an unparseable version, and
// the sentinel CLAUDE_CONFIG_DIR makes the command fail outright. This is
// the internal/process re-exec pattern: the test binary plays the CLI.
func runFakeClaude() {
	if envHasPrefix("FUGUE_VAULT_KEY=") || envHasPrefix("FUGUE_TEST_OUTER_SECRET=") || envHasPrefix("ANTHROPIC_API_KEY=") {
		writeOut("leaked-env\n")
		return
	}
	if !envHasPrefix("TERM=dumb") {
		writeOut("bad-term\n")
		return
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeFailConfigDir) {
		os.Exit(1)
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeHangConfigDir) {
		hangForever()
		return
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeFloodConfigDir) {
		floodOut()
		return
	}
	writeOut(fixtureCLI + " (Claude Code)\n")
}

// hangForever blocks until the process is killed. Waiting on a signal channel
// keeps the runtime's deadlock detector quiet, unlike a bare channel receive.
func hangForever() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}

// hangUntilStdinHUP blocks until stdin's write end is closed (POLLHUP) or SIGINT
// is received, without reading any data from stdin.
func pollStdinClosed(fds []unix.PollFd) bool {
	n, err := unix.Poll(fds, 50)
	if err != nil {
		return !errors.Is(err, unix.EINTR)
	}
	return n > 0 && (fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL)) != 0
}

func monitorStdinHUP(ctx context.Context, hupCh chan struct{}) {
	fds := []unix.PollFd{{Fd: 0, Events: 0}}
	for {
		if ctx.Err() != nil || pollStdinClosed(fds) {
			close(hupCh)
			return
		}
	}
}

func hangUntilStdinHUP() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	hupCh := make(chan struct{})
	_ = supervise.Go(context.Background(), "fake.stdin_hup", func(ctx context.Context) error {
		monitorStdinHUP(ctx, hupCh)
		return nil
	})
	select {
	case <-sig:
	case <-hupCh:
	}
}

// floodOut writes up to 64 MiB of stdout, stopping at the first write error
// (the probe killed it or closed the pipe).
func floodOut() {
	chunk := strings.Repeat("x", 64<<10)
	for range 1024 {
		if _, err := os.Stdout.WriteString(chunk); err != nil {
			return
		}
	}
}

// envHasPrefix reports whether the process environment holds an entry with
// the prefix; the fake reads os.Environ because os.Getenv belongs to
// internal/config (forbidigo).
func envHasPrefix(prefix string) bool {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func checkSpecialStartupEnv() bool {
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeCrashConfigDir) {
		os.Exit(1)
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeNoInitConfigDir) {
		hangForever()
		return true
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeCheckEnvConfigDir) {
		if envHasPrefix("FUGUE_") || envHasPrefix("ANTHROPIC_") || !envHasPrefix("TERM=dumb") {
			os.Exit(2)
		}
	}
	return false
}

func handleEarlyExitEnvs() bool {
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeFrameTooLargeConfigDir) {
		large := bytes.Repeat([]byte("X"), 8*1024*1024+1024)
		large = append(large, '\n')
		if _, err := os.Stdout.Write(large); err != nil {
			os.Exit(1)
		}
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			os.Exit(0)
		}
		return true
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakePrintArgvConfigDir) {
		argvBytes, err := json.Marshal(os.Args)
		if err != nil {
			os.Exit(1)
		}
		writeOut(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%s}]},"session_id":"uuid-fake"}`+"\n", string(argvBytes)))
		writeOut(`{"type":"result","subtype":"success","is_error":false,"result":"argv-printed","session_id":"uuid-fake"}` + "\n")
		return true
	}
	return false
}

func fakeInitJSON() string {
	version := fixtureCLI
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeUntestedVersionConfigDir) {
		version = "2.1.999"
	}
	return fmt.Sprintf(`{"type":"system","subtype":"init","session_id":"uuid-fake","tools":["Bash","Edit","Read","Task","Write"],"claude_code_version":"%s","model":"claude-sonnet-5-5","permissionMode":"default","slash_commands":["clear","compact"],"terminal_slash_commands":[]}`+"\n", version)
}

func startContinuousOutput() {
	supervise.Go(context.Background(), "fake.stdin_reader", func(taskCtx context.Context) error {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `"interrupt"`) {
				writeOut(`{"type":"result","subtype":"success","is_error":false,"result":"interrupted","session_id":"uuid-fake"}` + "\n")
			}
		}
		return nil
	})
	for i := 0; ; i++ {
		writeOut(fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"chunk-%d"}]},"session_id":"uuid-fake"}`+"\n", i))
	}
}

func runFakeClaudeSession() {
	if checkSpecialStartupEnv() {
		return
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeReplaySteerFixtureConfigDir) {
		runReplaySteerFixture()
		return
	}

	writeOut(fakeInitJSON())

	if handleEarlyExitEnvs() {
		return
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeNeverReadStdinConfigDir) {
		hangUntilStdinHUP()
		return
	}
	if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeContinuousOutputConfigDir) {
		startContinuousOutput()
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		handleFakeClaudeLine(scanner.Text())
	}
	os.Exit(0)
}

func handleFakeClaudeLine(line string) {
	if strings.Contains(line, `"subtype":"interrupt"`) {
		if envHasPrefix("CLAUDE_CONFIG_DIR=" + fakeIgnoreInterruptConfigDir) {
			hangForever()
			os.Exit(0)
		}
		writeOut(`{"type":"control_response","response":{"subtype":"success","request_id":"req-interrupt"}}` + "\n")
		writeOut(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"session_id":"uuid-fake"}` + "\n")
		writeOut(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"[Request interrupted by user]","session_id":"uuid-fake"}` + "\n")
		return
	}
	if strings.Contains(line, `"type":"user"`) {
		if strings.Contains(line, "interrupt-me") {
			writeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working..."}]},"session_id":"uuid-fake"}` + "\n")
			return
		}
		if strings.Contains(line, "steer-me") {
			writeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"waiting steer..."}]},"session_id":"uuid-fake"}` + "\n")
			return
		}
		writeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"fake reply"}]},"session_id":"uuid-fake"}` + "\n")
		writeOut(`{"type":"result","subtype":"success","is_error":false,"result":"fake reply","session_id":"uuid-fake"}` + "\n")
	}
}

func replayFixturePath() string {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR="+fakeReplaySteerFixtureConfigDir+":") {
			return strings.TrimPrefix(entry, "CLAUDE_CONFIG_DIR="+fakeReplaySteerFixtureConfigDir+":")
		}
	}
	return ""
}

func writeFixtureSlice(lines []string, start, end int) {
	for i := start; i < end && i < len(lines); i++ {
		writeOut(lines[i] + "\n")
	}
}

func scanSteerUUID(scanner *bufio.Scanner) string {
	if scanner.Scan() {
		var msg struct {
			Type string `json:"type"`
			UUID string `json:"uuid"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) == nil {
			return msg.UUID
		}
	}
	return ""
}

func emitRemainingFixture(lines []string, start int, steerUUID string) {
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if steerUUID != "" && strings.Contains(line, "uuid-0032") {
			line = strings.ReplaceAll(line, "uuid-0032", steerUUID)
		}
		writeOut(line + "\n")
	}
}

func runReplaySteerFixture() {
	fixturePath := replayFixturePath()
	if fixturePath == "" {
		os.Exit(1)
	}
	raw, err := os.ReadFile(filepath.Clean(fixturePath))
	if err != nil {
		os.Exit(1)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	writeFixtureSlice(lines, 0, 11)

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(1)
	}
	writeFixtureSlice(lines, 11, 22)

	steerUUID := scanSteerUUID(scanner)
	emitRemainingFixture(lines, 22, steerUUID)
	os.Exit(0)
}

func fakeClaudeExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}
