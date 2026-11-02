package sandbox

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"testing"

	"go.uber.org/goleak"
)

const (
	landlockChildFlag    = "-fugue-test-landlock-child"
	loopbackListenerFlag = "-fugue-test-loopback-listener"
	fakeSandboxExecFlag  = "-fugue-test-fake-sandbox"

	testEnvHome        = "HOME"
	testRelativePath   = "relative"
	testRelPath        = "rel"
	testCmdTrue        = "true"
	testBinTrue        = "/bin/true"
	testBinSh          = "/bin/sh"
	testSessionTempDir = "/private/tmp/dk-12345678"
	testWorkDir        = "/work"
	testEchoStdin      = "echo-stdin"
)

func TestMain(m *testing.M) {
	if code, handled := runPlatformChildHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	switch {
	case slices.Contains(os.Args[1:], loopbackListenerFlag):
		os.Exit(runLoopbackListenerHelper())
	case slices.Contains(os.Args[1:], fakeSandboxExecFlag):
		os.Exit(runFakeSandboxHelper())
	default:
		goleak.VerifyTestMain(m)
	}
}

func runLoopbackListenerHelper() int {
	var lc net.ListenConfig
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		listener, err := lc.Listen(context.Background(), "tcp", addr)
		if err != nil {
			writeQuietly(os.Stderr, err.Error()+"\n")
			return 2
		}
		closePipeQuietly(listener)
	}
	writeQuietly(os.Stdout, "LOOPBACK_LISTENERS_OK\n")
	return 0
}

func runFakeSandboxHelper() int {
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			target := args[i+1]
			if target == testEchoStdin {
				if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
					return 2
				}
				writeQuietly(os.Stderr, "piped-stderr")
				return 0
			}
			writeQuietly(os.Stdout, "stdout-ok")
			writeQuietly(os.Stderr, "stderr-ok")
			return 0
		}
	}
	writeQuietly(os.Stdout, "stdout-ok")
	return 0
}

func writeQuietly(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		return
	}
}

func boolPtr(v bool) *bool {
	return &v
}
