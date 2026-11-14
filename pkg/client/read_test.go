package client_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
)

// servingLine is what `fugue serve` writes to stdout once the socket
// answers; the tests block on it instead of polling a clock.
const servingLine = "fugue host serving at "

// buildFugue builds the fugue binary into t.TempDir(); a failed build is
// fatal for the calling test.
func buildFugue(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "go", "install", "./cmd/fugue")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "GOBIN="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fugue: %v: %s", err, out)
	}
	return filepath.Join(dir, "fugue")
}

// startRealHost runs `fugue serve` on a fresh data directory with short
// runtime paths (sun_path, as everywhere a socket is bound), waits for the
// serving line that names the socket, and returns a dialed client. It is a
// real host, vault locked.
func startRealHost(t *testing.T) *client.Client {
	t.Helper()
	base := shortDir(t)
	dataDir, runtime := filepath.Join(base, "data"), filepath.Join(base, "rt")
	for _, dir := range []string{dataDir, runtime} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := buildFugue(t)
	// The path is assigned, not passed, so the launch is audited as the
	// constant `go` spawn the lint rule allows (the same shape
	// internal/storage/lock_test.go uses for its helper).
	cmd := exec.CommandContext(t.Context(), "go")
	cmd.Path = bin
	cmd.Args = []string{bin, "serve"}
	cmd.Env = append(os.Environ(),
		"SYNX_FUGUE_DATA_DIR="+dataDir,
		"XDG_RUNTIME_DIR="+runtime,
	)
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			if kerr := cmd.Process.Kill(); kerr != nil {
				return
			}
		}
		// Wait reaps the process and ends exec's watch goroutine.
		if werr := cmd.Wait(); werr != nil {
			return
		}
	})
	socket, ok := socketFromServingLine(bufio.NewReader(stdout))
	if !ok {
		t.Fatal("the real host never wrote its serving line")
	}
	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatalf("dial the served socket %s: %v", socket, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// socketFromServingLine blocks until the host writes its serving line and
// returns the socket path it names.
func socketFromServingLine(reader *bufio.Reader) (string, bool) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", false
		}
		if socket, ok := strings.CutPrefix(line, servingLine); ok {
			return strings.TrimSpace(socket), true
		}
	}
}

// TestListSessionsAgainstARealHost reads the empty session list from a real
// `fugue serve` and reports its vendor-sessions sequence.
func TestListSessionsAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	list, err := c.ListSessions(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 0 {
		t.Fatalf("sessions = %v, want an empty list on a fresh host", list.Sessions)
	}
}

// TestTranscriptPageAgainstARealHost reads a page for a session the host does
// not know and gets the host's typed not-found answer back.
func TestTranscriptPageAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	_, err := c.TranscriptPage(testCtx(t), "no-such-session", 0, 0)
	if err == nil {
		t.Fatal("a page for an unknown session succeeded")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v, want the not-found code the host maps transcript.session_not_found to", err)
	}
}

// TestTranscriptItemAgainstARealHost reads one item of an unknown session and
// gets the host's typed not-found answer back.
func TestTranscriptItemAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	_, err := c.TranscriptItem(testCtx(t), "no-such-session", "no-such-item")
	if err == nil {
		t.Fatal("an item of an unknown session succeeded")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v, want the not-found code the host maps transcript.session_not_found to", err)
	}
}
