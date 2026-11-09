package claude

// Fixture hygiene: the repository is public, so a recording must not carry the
// recording machine's identity. The scan covers every file under testdata and
// fails on a home directory other than the REDACTED placeholder, a macOS home,
// a per-user runtime directory, a real UUID, an email address, a provider token
// shape, and any value under the environment-listing keys (plugins, MCP
// servers, memory paths) that is not an obvious placeholder. RECORDING.md
// describes the redaction pass this test verifies.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// \x2f is "/": the pattern is spelled without a literal home prefix so a
	// scan of the repository history for such prefixes finds only placeholders.
	homePathPattern  = regexp.MustCompile("\\x2fhome\\x2f([^/\"'`\\\\\\s,;)]+)")
	macHomePattern   = regexp.MustCompile(`/Users/`)
	runUserPattern   = regexp.MustCompile(`/run/user/\d+`)
	uuidPattern      = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	emailPattern     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	anthropicKeyExpr = regexp.MustCompile(`sk-ant-`)
	githubTokenExpr  = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]`)
)

// isEnvironmentKey reports whether a key lists the machine's own setup in a
// system/init event; its values must be placeholders in a committed fixture.
func isEnvironmentKey(key string) bool {
	switch key {
	case "plugins", "mcp_servers", "memory_paths":
		return true
	}
	return false
}

// placeholderValue reports whether a string under an environment key is an
// obvious placeholder: example-*, a REDACTED path, or a bare status word.
func placeholderValue(s string) bool {
	return strings.HasPrefix(s, "example-") ||
		strings.HasPrefix(s, "/home/REDACTED/") ||
		s == "connected"
}

// scanText returns one finding per host-identifying pattern in the text.
func scanText(text string) []string {
	var findings []string
	for _, m := range homePathPattern.FindAllStringSubmatch(text, -1) {
		if m[1] != "REDACTED" {
			findings = append(findings, "home directory "+m[0])
		}
	}
	checks := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"macOS home directory", macHomePattern},
		{"per-user runtime directory", runUserPattern},
		{"UUID", uuidPattern},
		{"email address", emailPattern},
		{"Anthropic key prefix", anthropicKeyExpr},
		{"GitHub token prefix", githubTokenExpr},
	}
	for _, c := range checks {
		if loc := c.re.FindStringIndex(text); loc != nil {
			findings = append(findings, c.name+" at offset "+strconv.Itoa(loc[0]))
		}
	}
	return findings
}

// scanEnvironmentKeys walks decoded JSON and reports every string leaf under
// an environment key that is not a placeholder.
func scanEnvironmentKeys(v any, inside bool) []string {
	var findings []string
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			findings = append(findings, scanEnvironmentKeys(child, inside || isEnvironmentKey(k))...)
		}
	case []any:
		for _, child := range t {
			findings = append(findings, scanEnvironmentKeys(child, inside)...)
		}
	case string:
		if inside && !placeholderValue(t) {
			findings = append(findings, "non-placeholder value "+t)
		}
	}
	return findings
}

// maxHookOutput bounds the output and stdout of a recorded hook_response: real
// hook output is the installed plugins' own text and does not belong in a
// public fixture.
const maxHookOutput = 512

// realEnvironmentNames are names of real plugins and servers seen on a
// recording machine. They are spelled in pieces so this source does not carry
// them itself.
func realEnvironmentNames() []string {
	return []string{
		"super" + "powers",
		"atlas" + "sian",
		"ser" + "ena",
		"claude-plugins-" + "official",
		"EXTREME" + "LY_IMPORTANT",
	}
}

// hookOutputFindings checks one hook_response object.
func hookOutputFindings(event map[string]any) []string {
	if event["subtype"] != "hook_response" {
		return nil
	}
	var findings []string
	for _, key := range []string{"output", "stdout"} {
		if s, ok := event[key].(string); ok && len(s) > maxHookOutput {
			findings = append(findings, fmt.Sprintf("hook_response %s is %d bytes, over %d", key, len(s), maxHookOutput))
		}
	}
	return findings
}

// scanHookOutput reports hook_response events, at any depth, whose output or
// stdout exceeds the bound.
func scanHookOutput(v any) []string {
	var findings []string
	switch t := v.(type) {
	case map[string]any:
		findings = append(findings, hookOutputFindings(t)...)
		for _, child := range t {
			findings = append(findings, scanHookOutput(child)...)
		}
	case []any:
		for _, child := range t {
			findings = append(findings, scanHookOutput(child)...)
		}
	}
	return findings
}

// scanRealNames reports every real environment name in the text.
func scanRealNames(text string) []string {
	var findings []string
	lower := strings.ToLower(text)
	for _, real := range realEnvironmentNames() {
		if strings.Contains(lower, strings.ToLower(real)) {
			findings = append(findings, "real environment name "+real)
		}
	}
	return findings
}

// jsonDocuments splits a file into the JSON documents it holds: one per line
// for .jsonl, the whole file for .json, none otherwise.
func jsonDocuments(name string, raw []byte) [][]byte {
	switch filepath.Ext(name) {
	case ".jsonl":
		var documents [][]byte
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				documents = append(documents, []byte(line))
			}
		}
		return documents
	case ".json":
		return [][]byte{raw}
	}
	return nil
}

// scanFile scans one testdata file: the text patterns and real names on every
// file, the environment keys and hook output on every JSON document.
func scanFile(name string, raw []byte) []string {
	findings := scanText(string(raw))
	findings = append(findings, scanRealNames(string(raw))...)
	for _, doc := range jsonDocuments(name, raw) {
		var decoded any
		if json.Unmarshal(doc, &decoded) != nil {
			findings = append(findings, "not valid JSON")
			continue
		}
		findings = append(findings, scanEnvironmentKeys(decoded, false)...)
		findings = append(findings, scanHookOutput(decoded)...)
	}
	for i := range findings {
		findings[i] = name + ": " + findings[i]
	}
	return findings
}

func TestFixturesContainNoHostIdentity(t *testing.T) {
	t.Parallel()
	scanned := 0
	root := os.DirFS("testdata")
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk testdata: %w", walkErr)
		}
		if d.IsDir() {
			return nil
		}
		raw, readErr := fs.ReadFile(root, path)
		if readErr != nil {
			return fmt.Errorf("read testdata/%s: %w", path, readErr)
		}
		scanned++
		for _, f := range scanFile(path, raw) {
			t.Error(f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("no testdata files scanned")
	}
}

// TestHostIdentityScanFlagsWhatItClaimsTo proves each detector fires, so the
// hygiene test cannot pass by scanning nothing.
func TestHostIdentityScanFlagsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	dirty := map[string]string{
		"home":    `{"cwd":"/hom` + `e/someone/project"}`,
		"mac":     `{"cwd":"/Users/someone/project"}`,
		"runtime": `{"socket":"/run/user/` + `1000/app.sock"}`,
		"uuid":    `{"id":"123e4567-e89b-12d3-a456-426614174000"}`,
		"email":   `{"who":"person@example.org"}`,
		"anthro":  `{"key":"sk-ant-abc"}`,
		"github":  `{"token":"ghp_abc123"}`,
	}
	for name, text := range dirty {
		if len(scanText(text)) == 0 {
			t.Errorf("%s: the text scan missed %s", name, text)
		}
	}
	bigHook := `{"subtype":"hook_response","output":"` + strings.Repeat("x", maxHookOutput+1) + `"}`
	if got := scanFile("x.jsonl", []byte(bigHook)); len(got) != 1 {
		t.Errorf("an oversized hook output was not flagged exactly once: %v", got)
	}
	if got := scanFile("x.jsonl", []byte(`{"subtype":"hook_response","output":"{}"}`)); len(got) != 0 {
		t.Errorf("a short hook output was flagged: %v", got)
	}
	for _, name := range realEnvironmentNames() {
		if got := scanFile("x.json", []byte(`{"v":"`+name+`"}`)); len(got) == 0 {
			t.Errorf("the real name %q was not flagged", name)
		}
	}
	clean := `{"cwd":"/home/REDACTED/claude-fixture","id":"uuid-0001"}`
	if got := scanText(clean); len(got) != 0 {
		t.Errorf("the placeholder form was flagged: %v", got)
	}

	realistic := `{"plugins":[{"name":"my-plugin","path":"/home/REDACTED/p"}],"mcp_servers":[{"name":"example-mcp","status":"connected"}]}`
	if got := scanFile("x.json", []byte(realistic)); len(got) != 1 {
		t.Errorf("want exactly the non-placeholder plugin name flagged, got %v", got)
	}
	fake := `{"plugins":[{"name":"example-plugin","path":"/home/REDACTED/example-plugin"}],"memory_paths":["/home/REDACTED/memory"]}`
	if got := scanFile("x.json", []byte(fake)); len(got) != 0 {
		t.Errorf("placeholder environment values were flagged: %v", got)
	}
}
