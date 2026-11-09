package claude

import (
	"flag"
	"os"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// The fake claude's fail sentinel: a CLAUDE_CONFIG_DIR that makes the
// version command exit non-zero. fixtureCLI is the pinned version the
// fixtures were recorded from.
const (
	fakeFailConfigDir = "/probe/fail"
	fixtureCLI        = "2.1.319"
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
		return
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
	writeOut(fixtureCLI + " (Claude Code)\n")
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
