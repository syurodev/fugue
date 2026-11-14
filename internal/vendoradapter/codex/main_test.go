package codex

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/process"
)

const (
	fakeFailConfigDir            = "/probe/fail"
	fakeHangConfigDir            = "/probe/hang"
	fakeFloodConfigDir           = "/probe/flood"
	fakeUntestedVersionConfigDir = "/probe/untested-version"
	fakeNoInitConfigDir          = "/session/no-init"
	fakeIgnoreInterruptConfigDir = "/session/ignore-interrupt"
	fakeFrameTooLargeConfigDir   = "/session/frame-too-large"
	fakeSetsidChildConfigDir     = "/session/setsid-child"
	fakeCheckEnvConfigDir        = "/session/check-env"
	fakeApprovalConfigDir        = "/session/approval"
	fakeUserInputConfigDir       = "/session/user-input"
	fakeRecordRPCsConfigDir      = "/session/record-rpcs"
	fixtureCLI                   = "0.167.0"
	untestedCLI                  = "0.168.0"
)

func writeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		runFakeCodexVersion()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		runFakeCodexAppServer()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "setsid-child" {
		hangForever()
		os.Exit(0)
	}
	flag.Bool("update", false, "rewrite the golden transcript files")
	goleak.VerifyTestMain(m)
}

func goldensRequested() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

func runFakeCodexVersion() {
	if envHasPrefix("FUGUE_VAULT_KEY=") || envHasPrefix("FUGUE_TEST_OUTER_SECRET=") || envHasPrefix("OPENAI_API_KEY=") || envHasPrefix("ANTHROPIC_API_KEY=") {
		writeOut("leaked-env\n")
		return
	}
	if !envHasPrefix("TERM=dumb") {
		writeOut("bad-term\n")
		return
	}
	if envHasPrefix("CODEX_HOME=" + fakeFailConfigDir) {
		os.Exit(1)
	}
	if envHasPrefix("CODEX_HOME=" + fakeHangConfigDir) {
		hangForever()
		return
	}
	if envHasPrefix("CODEX_HOME=" + fakeFloodConfigDir) {
		floodOut()
		return
	}
	if envHasPrefix("CODEX_HOME=" + fakeUntestedVersionConfigDir) {
		writeOut("codex-cli " + untestedCLI + "\n")
		return
	}
	writeOut("codex-cli " + fixtureCLI + "\n")
}

func hangForever() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

func floodOut() {
	chunk := strings.Repeat("x", 64<<10)
	for range 1024 {
		if _, err := os.Stdout.WriteString(chunk); err != nil {
			return
		}
	}
}

func envHasPrefix(prefix string) bool {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func codexHomeEnv() string {
	for _, entry := range os.Environ() {
		if after, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			return after
		}
	}
	return ""
}

func runFakeCodexAppServer() {
	home := codexHomeEnv()
	if home == fakeCheckEnvConfigDir {
		if envHasPrefix("FUGUE_SECRET") || envHasPrefix("FUGUE_VAULT") || envHasPrefix("OPENAI_") || envHasPrefix("ANTHROPIC_") || !envHasPrefix("TERM=dumb") {
			os.Exit(2)
		}
	}
	if home == fakeNoInitConfigDir {
		hangForever()
		return
	}
	if home == fakeFrameTooLargeConfigDir {
		writeOut(strings.Repeat("a", process.MaxFrameSize+1024) + "\n")
		return
	}
	if strings.HasPrefix(home, fakeSetsidChildConfigDir+":") {
		pidFile := strings.TrimPrefix(home, fakeSetsidChildConfigDir+":")
		spawnSetsidChild(pidFile)
		serveFakeAppServerLoop(home)
		// Keep the leader alive across stdin EOF so Close kills the process
		// tree cleanly via Stop rather than racing leader exit against SIGTERM.
		waitUntilSignaled()
		return
	}
	serveFakeAppServerLoop(home)
}

func waitUntilSignaled() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	<-sig
}

func spawnSetsidChild(pidFile string) {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	proc, err := os.StartProcess(exe, []string{exe, "setsid-child"}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		os.Exit(1)
	}
	if wErr := os.WriteFile(filepath.Clean(pidFile), []byte(strconv.Itoa(proc.Pid)), 0o600); wErr != nil {
		os.Exit(1)
	}
}

func serveFakeAppServerLoop(home string) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var rpcLogPath string
	if strings.HasPrefix(home, fakeRecordRPCsConfigDir+":") {
		rpcLogPath = strings.TrimPrefix(home, fakeRecordRPCsConfigDir+":")
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if rpcLogPath != "" {
			appendRPCLog(rpcLogPath, line)
		}
		var msg wireMessage
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		handleFakeAppServerMessage(home, &msg)
	}
}

func appendRPCLog(path string, line []byte) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if _, wErr := f.Write(append(append([]byte(nil), line...), '\n')); wErr != nil {
		os.Exit(1)
	}
	if cErr := f.Close(); cErr != nil {
		os.Exit(1)
	}
}

func handleFakeAppServerMessage(home string, msg *wireMessage) {
	id := string(msg.ID)
	switch msg.Method {
	case MethodInitialize:
		ver := fixtureCLI
		if home == fakeUntestedVersionConfigDir {
			ver = untestedCLI
		}
		writeOut(fmt.Sprintf(`{"id":%s,"result":{"userAgent":"codex_cli_rs/%s (Linux; x86_64) terminal","codexHome":"/home/REDACTED/.codex","platformFamily":"unix","platformOs":"linux"}}`+"\n", id, ver))
	case MethodInitialized:
		return
	case MethodRateLimitsRead:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{"rateLimits":{"limitId":"codex","limitName":null,"planType":"pro","primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1800000000},"secondary":null,"rateLimitReachedType":null},"rateLimitsByLimitId":{}}}`+"\n", id))
	case MethodThreadStart, MethodThreadResume:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{"thread":{"id":"019a8000-0000-7000-8000-000000000001","name":null,"cwd":"/tmp"},"model":"gpt-5.4","approvalPolicy":"on-request","reasoningEffort":null}}`+"\n", id))
	case MethodTurnStart, MethodReviewStart, MethodThreadCompactStart:
		handleFakeTurnStart(home, id, msg.Method)
	case MethodTurnSteer:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{"turnId":"turn-codex-1"}}`+"\n", id))
		writeOut(`{"method":"item/agentMessage/delta","params":{"threadId":"019a8000-0000-7000-8000-000000000001","turnId":"turn-codex-1","itemId":"msg-1","delta":"STEERED"}}` + "\n")
	case MethodTurnInterrupt:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{}}`+"\n", id))
		if home != fakeIgnoreInterruptConfigDir {
			writeOut(`{"method":"turn/completed","params":{"threadId":"019a8000-0000-7000-8000-000000000001","turn":{"id":"turn-codex-1","status":"interrupted","error":null}}}` + "\n")
		}
	}
}

func handleFakeTurnStart(home, id, method string) {
	switch method {
	case MethodThreadCompactStart:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{}}`+"\n", id))
	case MethodReviewStart:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"turn-codex-1","status":"inProgress","error":null},"reviewThreadId":"019a8000-0000-7000-8000-000000000001"}}`+"\n", id))
	default:
		writeOut(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"turn-codex-1","status":"inProgress","error":null}}}`+"\n", id))
	}
	writeOut(`{"method":"turn/started","params":{"threadId":"019a8000-0000-7000-8000-000000000001","turn":{"id":"turn-codex-1","status":"inProgress","error":null}}}` + "\n")
	writeOut(`{"method":"item/started","params":{"threadId":"019a8000-0000-7000-8000-000000000001","turnId":"turn-codex-1","item":{"type":"agentMessage","id":"msg-1","text":"","phase":null}}}` + "\n")
	if home == fakeIgnoreInterruptConfigDir {
		return
	}
	if strings.HasPrefix(home, fakeRecordRPCsConfigDir+":") {
		writeOut(`{"method":"item/completed","params":{"threadId":"019a8000-0000-7000-8000-000000000001","turnId":"turn-codex-1","item":{"type":"agentMessage","id":"msg-1","text":"OK","phase":null}}}` + "\n")
		writeOut(`{"method":"turn/completed","params":{"threadId":"019a8000-0000-7000-8000-000000000001","turn":{"id":"turn-codex-1","status":"completed","error":null}}}` + "\n")
	}
}
