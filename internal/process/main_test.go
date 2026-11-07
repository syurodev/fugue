package process

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/goleak"
)

const fakeVendorFlag = "-fugue-test-fake-vendor"

func TestMain(m *testing.M) {
	for i, arg := range os.Args {
		if arg == fakeVendorFlag && i+1 < len(os.Args) {
			runFakeVendor(os.Args[i+1])
			return
		}
	}
	goleak.VerifyTestMain(m)
}

func runFakeVendor(mode string) {
	switch mode {
	case "protocol-interrupt":
		runProtocolInterrupt()
	case "ignore-sigint":
		runIgnoreSigint()
	case "ignore-sigterm":
		runIgnoreSigterm()
	case "frame-too-large":
		runFrameTooLarge()
	case "flood-stderr":
		runFloodStderr()
	case "exit-on-eof":
		runExitOnEOF()
	case "normal", "sleeper":
		runNormal()
	default:
		os.Exit(3)
	}
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
	select {}
}

func runExitOnEOF() {
	writeQuietly(os.Stdout, "READY\n")
	if _, err := io.ReadAll(os.Stdin); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func runNormal() {
	writeQuietly(os.Stdout, "READY\n")
	select {}
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
