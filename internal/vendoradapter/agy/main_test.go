package agy

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
	fakeBadProtoConfigDir        = "/session/bad-proto"
	fakeNoInitConfigDir          = "/session/no-init"
	fakeIgnoreCancelConfigDir    = "/session/ignore-cancel"
	fakeFrameTooLargeConfigDir   = "/session/frame-too-large"
	fakeSetsidChildConfigDir     = "/session/setsid-child"
	fakeCheckEnvConfigDir        = "/session/check-env"
	fakePermissionConfigDir      = "/session/permission"
	fakeOtherRequestsConfigDir   = "/session/other-requests"
	fakeCancelWaitConfigDir      = "/session/cancel-wait"
	fakeRecordRPCsConfigDir      = "/session/record-rpcs"
	fixtureCLI                   = "1.5.9"
	untestedCLI                  = "1.6.0"
	acpArg                       = "acp"
)

func writeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		runFakeAgyVersion()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == acpArg {
		runFakeAgyACP()
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

func runFakeAgyVersion() {
	if envHasPrefix("FUGUE_VAULT_KEY=") || envHasPrefix("FUGUE_TEST_OUTER_SECRET=") || envHasPrefix("OPENAI_API_KEY=") || envHasPrefix("ANTHROPIC_API_KEY=") {
		writeOut("leaked-env\n")
		return
	}
	if !envHasPrefix("TERM=dumb") {
		writeOut("bad-term\n")
		return
	}
	switch agyHomeEnv() {
	case fakeFailConfigDir:
		os.Exit(1)
	case fakeHangConfigDir:
		hangForever()
	case fakeFloodConfigDir:
		floodOut()
	case fakeUntestedVersionConfigDir:
		writeOut(untestedCLI + "\n")
	default:
		writeOut(fixtureCLI + "\n")
	}
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

func agyHomeEnv() string {
	for _, entry := range os.Environ() {
		if after, ok := strings.CutPrefix(entry, "HOME="); ok {
			return after
		}
	}
	return ""
}

func runFakeAgyACP() {
	home := agyHomeEnv()
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
		serveFakeACPLoop(home)
		waitUntilSignaled()
		return
	}
	serveFakeACPLoop(home)
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

func serveFakeACPLoop(home string) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var rpcLogPath string
	if path, ok := strings.CutPrefix(home, fakeRecordRPCsConfigDir+":"); ok {
		rpcLogPath = path
	} else if path, ok := strings.CutPrefix(home, fakePermissionConfigDir+":"); ok {
		rpcLogPath = path
	} else if path, ok := strings.CutPrefix(home, fakeOtherRequestsConfigDir+":"); ok {
		rpcLogPath = path
	}
	var activePromptID string
	for scanner.Scan() {
		line := scanner.Bytes()
		if rpcLogPath != "" {
			appendRPCLog(rpcLogPath, line)
		}
		var msg wireMessage
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		activePromptID = handleFakeACPMessage(home, &msg, activePromptID)
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

func handleFakeACPMessage(home string, msg *wireMessage, activePromptID string) string {
	id := string(msg.ID)
	switch msg.Method {
	case MethodInitialize:
		writeFakeInitialize(home, id)
	case MethodSessionNew, MethodSessionLoad:
		writeFakeSessionOpen(msg.Method, msg.Params, id)
	case MethodSessionSetConfigOption:
		writeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"configOptions":[]}}`+"\n", id))
	case MethodSessionPrompt:
		return handleFakePrompt(home, id)
	case MethodSessionCancel:
		if home != fakeIgnoreCancelConfigDir && activePromptID != "" {
			writeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"cancelled"}}`+"\n", activePromptID))
			return ""
		}
	}
	return activePromptID
}

func writeFakeInitialize(home, id string) {
	protoVer := ProtocolVersion
	ver := fixtureCLI
	switch home {
	case fakeBadProtoConfigDir:
		protoVer = 99
	case fakeUntestedVersionConfigDir:
		ver = untestedCLI
	}
	writeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%d,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true,"audio":false,"embeddedContext":true},"sessionCapabilities":{"resume":{},"close":{},"load":{},"list":{},"additionalDirectories":{}},"_meta":{"antigravity":{"tier":true}}},"agentInfo":{"name":"Antigravity ACP Server","version":%q},"_meta":{"antigravity":{"safety":"ignored"}}}}`+"\n", id, protoVer, ver))
}

func writeFakeSessionOpen(method string, params json.RawMessage, id string) {
	sid := "session-uuid-0001"
	if method == MethodSessionLoad {
		var p SessionLoadParams
		if json.Unmarshal(params, &p) == nil && p.SessionID != "" {
			sid = p.SessionID
		}
		writeOut(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + sid + `","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"prior turn"}}}}` + "\n")
	}
	writeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":%q,"models":{"availableModels":[{"modelId":"gemini-3.8-flash","name":"Gemini 3.8 Flash"}],"currentModelId":"gemini-3.8-flash"},"modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"}]},"configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"gemini-3.8-flash","options":[{"value":"gemini-3.8-flash","name":"Gemini 3.8 Flash"}]}],"_meta":{"antigravity":{"tier":{"id":"free"}}}}}`, id, sid) + "\n")
}

func handleFakePrompt(home, id string) string {
	switch {
	case home == fakePermissionConfigDir || strings.HasPrefix(home, fakePermissionConfigDir+":"):
		writeOut(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-uuid-0001","update":{"sessionUpdate":"tool_call","toolCallId":"call-0001","name":"run_command","kind":"execute","status":"pending","title":"echo hi"}}}` + "\n")
		writeOut(`{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"session-uuid-0001","toolCall":{"sessionUpdate":"tool_call","toolCallId":"call-0001","name":"run_command","kind":"execute","status":"pending"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"deny","name":"Deny","kind":"reject_once"}]}}` + "\n")
		return id
	case strings.HasPrefix(home, fakeOtherRequestsConfigDir+":"):
		writeOut(`{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"session-uuid-0001","toolCall":{"sessionUpdate":"tool_call","toolCallId":"call-0001","name":"run_command","kind":"execute","status":"pending"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}` + "\n")
		writeOut(`{"jsonrpc":"2.0","id":42,"method":"fs/read_text_file","params":{"sessionId":"session-uuid-0001","path":"README.md"}}` + "\n")
		writeOut(`{"jsonrpc":"2.0","id":"req-str-9","method":"custom/unknown_method","params":{"sessionId":"session-uuid-0001"}}` + "\n")
		writeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", id))
		return ""
	case home == fakeCancelWaitConfigDir || home == fakeIgnoreCancelConfigDir:
		writeOut(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-uuid-0001","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"working..."}}}}` + "\n")
		return id
	default:
		writeOut(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-uuid-0001","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"PONG"}}}}` + "\n")
		writeOut(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":1,"cachedReadTokens":0}}}`+"\n", id))
		return ""
	}
}
