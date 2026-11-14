package hostapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

// Prompts that make the fake vendor act like a vendor process would: report
// what it was started with, try to be a local client, or run the MCP bridge
// its --mcp-config names. Each writes its result into the working directory.
const (
	fakeReport     = "fake-report"
	fakeHello      = "fake-hello"
	fakeBridge     = "fake-bridge"
	fakeReportFile = "fake-report.json"
	fakeHelloFile  = "fake-hello.txt"
	fakeBridgeFile = "fake-bridge.json"
)

// fakeVendorReport is what the fake saw at startup. It never writes the
// token itself, only its digest.
type fakeVendorReport struct {
	Args        []string `json:"args"`
	EnvKeys     []string `json:"env_keys"`
	TokenSHA256 string   `json:"token_sha256"`
}

type fakeBridgeRun struct {
	Replies []json.RawMessage `json:"replies"`
	Exit    int               `json:"exit"`
	Error   string            `json:"error,omitempty"`
}

// fakeVendorAction runs the action a prompt asks for, if any.
func fakeVendorAction(line string) {
	switch {
	case strings.Contains(line, fakeReport):
		fakeWriteJSON(fakeReportFile, fakeStartupReport())
	case strings.Contains(line, fakeHello):
		fakeWrite(fakeHelloFile, []byte(fakeLocalHello()))
	case strings.Contains(line, fakeBridge):
		fakeWriteJSON(fakeBridgeFile, fakeRunBridge())
	}
}

func fakeStartupReport() fakeVendorReport {
	report := fakeVendorReport{Args: os.Args}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		report.EnvKeys = append(report.EnvKeys, key)
		if key == app.SessionTokenEnv {
			sum := sha256.Sum256([]byte(value))
			report.TokenSHA256 = hex.EncodeToString(sum[:])
		}
	}
	slices.Sort(report.EnvKeys)
	return report
}

// fakeBridgeServer is the bridge's entry of --mcp-config.
type fakeBridgeServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// fakeBridgeCommand reads the bridge command line out of --mcp-config.
func fakeBridgeCommand() (string, []string) {
	server := fakeBridgeEntry()
	return server.Command, server.Args
}

func fakeBridgeEntry() fakeBridgeServer {
	i := slices.Index(os.Args, "--mcp-config")
	if i < 0 || i+1 >= len(os.Args) {
		return fakeBridgeServer{}
	}
	var cfg struct {
		Servers map[string]fakeBridgeServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(os.Args[i+1]), &cfg); err != nil {
		return fakeBridgeServer{}
	}
	return cfg.Servers["fugue"]
}

// fakeBridgeEnv is the environment Claude Code gives a stdio server: a small
// fixed set, plus the config's env entries with ${NAME} expanded from the
// CLI's own environment. It does not pass the whole environment on, so the
// bridge gets its token only through the config entry.
func fakeBridgeEnv(server fakeBridgeServer) []string {
	own := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		own[key] = value
	}
	env := make([]string, 0, 2+len(server.Env))
	env = append(env, "PATH="+own["PATH"], "HOME="+own["HOME"])
	for name, value := range server.Env {
		env = append(env, name+"="+os.Expand(value, func(key string) string { return own[key] }))
	}
	return env
}

// fakeLocalHello dials the host socket as a desktop app would and reports
// what the hello returned: a vendor process trying to be a local client.
func fakeLocalHello() string {
	_, args := fakeBridgeCommand()
	if len(args) != 3 {
		return "no socket in --mcp-config"
	}
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: &protos, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", args[2])
	}}}
	defer client.CloseIdleConnections()
	_, err := hostv1connect.NewHostServiceClient(client, "http://fugue").Hello(context.Background(), connect.NewRequest(&hostv1.HelloRequest{
		ClientKind: hostv1.ClientKind_CLIENT_KIND_APP, ClientName: "vendor-child", SupportedApiVersions: []string{"v1"},
	}))
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String() + ": " + err.Error()
}

// fakeBridgeRequests is the MCP conversation the fake holds with the bridge.
func fakeBridgeRequests() []string {
	return []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"fake-vendor","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"call-3","method":"tools/call","params":{"name":"fugue_test_echo","arguments":{"msg":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fugue_test_absent","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":6,"method":"ping"}`,
	}
}

// fakeRunBridge spawns the bridge exactly as the vendor CLI would — the
// command of --mcp-config with the environment Claude Code builds — and talks MCP
// to it, one request at a time, then closes its stdin.
func fakeRunBridge() fakeBridgeRun {
	server := fakeBridgeEntry()
	if server.Command == "" {
		return fakeBridgeRun{Exit: -1, Error: "no bridge in --mcp-config"}
	}
	// The command comes from the host's own --mcp-config, as for the real CLI.
	cmd := &exec.Cmd{Path: server.Command, Args: append([]string{server.Command}, server.Args...), Env: fakeBridgeEnv(server)}
	stdin, inErr := cmd.StdinPipe()
	stdout, outErr := cmd.StdoutPipe()
	if inErr != nil || outErr != nil {
		return fakeBridgeRun{Exit: -1, Error: "bridge pipes"}
	}
	if err := cmd.Start(); err != nil {
		return fakeBridgeRun{Exit: -1, Error: "start the bridge: " + err.Error()}
	}
	run := fakeBridgeRun{}
	replies := bufio.NewReader(stdout)
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		run.Error = err.Error()
	}
	for _, request := range fakeBridgeRequests() {
		if _, err := io.WriteString(stdin, request+"\n"); err != nil {
			run.Error = err.Error()
			break
		}
		reply, err := replies.ReadBytes('\n')
		if err != nil {
			run.Error = "read the bridge reply: " + err.Error()
			break
		}
		run.Replies = append(run.Replies, json.RawMessage(reply))
	}
	if err := stdin.Close(); err != nil {
		run.Error = err.Error()
	}
	if err := cmd.Wait(); err != nil {
		run.Error = "bridge exit: " + err.Error()
	}
	run.Exit = cmd.ProcessState.ExitCode()
	return run
}

func fakeWriteJSON(name string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		os.Exit(1)
	}
	fakeWrite(name, raw)
}

func fakeWrite(name string, data []byte) {
	if err := os.WriteFile(name, data, 0o600); err != nil {
		os.Exit(1)
	}
}
