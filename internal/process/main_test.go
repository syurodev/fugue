package process

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/goleak"
)

const (
	fakeVendorFlag              = "-fugue-test-fake-vendor"
	fakeVendorModeIgnoreSigterm = "ignore-sigterm"
	nonexistentSystemdRun       = "/nonexistent/systemd-run"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "--user" && os.Args[2] == "--scope" {
		runFakeVendor("fake-systemd-run")
		return
	}
	for i, arg := range os.Args {
		if arg == fakeVendorFlag && i+1 < len(os.Args) {
			runFakeVendor(os.Args[i+1])
			return
		}
	}
	goleak.VerifyTestMain(m)
}

func runFakeVendor(mode string) {
	modes := map[string]func(){
		"protocol-interrupt":        runProtocolInterrupt,
		"ignore-sigint":             runIgnoreSigint,
		fakeVendorModeIgnoreSigterm: runIgnoreSigterm,
		"frame-too-large":           runFrameTooLarge,
		"flood-stderr":              runFloodStderr,
		"exit-on-eof":               runExitOnEOF,
		"burst-and-exit":            runBurstAndExit,
		"group-child-worker":        runGroupChildWorker,
		"group-child-survivor":      runGroupChildSurvivor,
		fakeVendorModeNormal:        runNormal,
	}
	for name, fn := range unixModes() {
		modes[name] = fn
	}
	run, ok := modes[mode]
	if !ok {
		os.Exit(3)
	}
	run()
}

func writeQuietly(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		os.Exit(1)
	}
}

func runProtocolInterrupt() {
	writeQuietly(os.Stdout, "READY\n")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() && strings.TrimSpace(scanner.Text()) == "interrupt" {
		os.Exit(0)
	}
	os.Exit(0)
}

func runIgnoreSigint() {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT)
	writeQuietly(os.Stdout, "READY\n")
	for {
		sig := <-sigCh
		if sig == syscall.SIGINT {
			continue
		}
	}
}

func runIgnoreSigterm() {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	writeQuietly(os.Stdout, "READY\n")
	for {
		<-sigCh
	}
}

func runFrameTooLarge() {
	largePayload := bytes.Repeat([]byte("A"), MaxFrameSize+1024)
	largePayload = append(largePayload, '\n')
	if _, err := os.Stdout.Write(largePayload); err != nil {
		os.Exit(1)
	}
}

func runFloodStderr() {
	chunk := bytes.Repeat([]byte("E"), 1024)
	for range 256 {
		if _, err := os.Stderr.Write(chunk); err != nil {
			os.Exit(1)
		}
	}
	writeQuietly(os.Stdout, "FLOOD_DONE\n")
	blockForever()
}

func runExitOnEOF() {
	writeQuietly(os.Stdout, "READY\n")
	if _, err := io.ReadAll(os.Stdin); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func runBurstAndExit() {
	for i := range 499 {
		writeQuietly(os.Stdout, fmt.Sprintf("line %03d\n", i))
	}
	largePayload := bytes.Repeat([]byte("Z"), 64*1024)
	writeQuietly(os.Stdout, fmt.Sprintf("line 499 %s\n", string(largePayload)))
	os.Exit(0)
}

func runGroupChildWorker() {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	writeQuietly(os.Stdout, "CHILD_READY\n")
	for {
		<-sigCh
	}
}

func runGroupChildSurvivor() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	child := &exec.Cmd{
		Path: exe,
		Args: []string{exe, fakeVendorFlag, "group-child-worker"},
	}
	childOut, pipeErr := child.StdoutPipe()
	if pipeErr != nil {
		os.Exit(1)
	}
	if startErr := child.Start(); startErr != nil {
		os.Exit(1)
	}
	r := bufio.NewReader(childOut)
	readyLine, readErr := r.ReadString('\n')
	if readErr != nil || strings.TrimSpace(readyLine) != "CHILD_READY" {
		os.Exit(1)
	}

	writeQuietly(os.Stdout, fmt.Sprintf("CHILD %d\n", child.Process.Pid))
	writeQuietly(os.Stdout, "READY\n")

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT)
	for {
		sig := <-sigCh
		if sig == syscall.SIGINT {
			os.Exit(0)
		}
	}
}

func runNormal() {
	writeQuietly(os.Stdout, "READY\n")
	blockForever()
}

func fakeVendorExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

func waitReady(t *testing.T, lr *LineReader) {
	t.Helper()
	line, err := lr.ReadLine()
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if string(line) != "READY" {
		t.Fatalf("waitReady expected READY, got %q", string(line))
	}
}
