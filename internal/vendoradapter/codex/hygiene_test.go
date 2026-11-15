package codex

// Fixture hygiene: the repository is public, so a recording must not carry the
// recording machine's identity. TestCodexFixturesContainNoHostIdentity scans
// every file under testdata/ and fails on any home directory other than the
// REDACTED placeholder, a macOS home, a per-user runtime directory, a real UUID,
// an email address, or a provider token prefix.

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
	homePathPattern  = regexp.MustCompile("\\x2fhome\\x2f([^/\"'`\\\\\\s,;)]+)")
	macHomePattern   = regexp.MustCompile(`/Users/`)
	varFoldersExpr   = regexp.MustCompile(`/var/folders/`)
	runUserPattern   = regexp.MustCompile(`/run/user/\d+`)
	uuidPattern      = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	emailPattern     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	anthropicKeyExpr = regexp.MustCompile(`sk-ant-`)
	openAIKeyExpr    = regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9]{12,}`)
	githubTokenExpr  = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]`)
)

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
		{"macOS temp directory", varFoldersExpr},
		{"per-user runtime directory", runUserPattern},
		{"UUID", uuidPattern},
		{"email address", emailPattern},
		{"Anthropic key prefix", anthropicKeyExpr},
		{"OpenAI key prefix", openAIKeyExpr},
		{"GitHub token prefix", githubTokenExpr},
	}
	for _, c := range checks {
		if loc := c.re.FindStringIndex(text); loc != nil {
			findings = append(findings, c.name+" at offset "+strconv.Itoa(loc[0]))
		}
	}
	return findings
}

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

func scanFile(name string, raw []byte) []string {
	findings := scanText(string(raw))
	for _, doc := range jsonDocuments(name, raw) {
		var decoded any
		if json.Unmarshal(doc, &decoded) != nil {
			findings = append(findings, "not valid JSON")
		}
	}
	for i := range findings {
		findings[i] = name + ": " + findings[i]
	}
	return findings
}

func TestCodexFixturesContainNoHostIdentity(t *testing.T) {
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

func TestCodexHostIdentityScanFlagsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	dirty := map[string]string{
		"home":    `{"cwd":"/hom` + `e/someone/project"}`,
		"mac":     `{"cwd":"/Users/someone/project"}`,
		"var":     `{"cwd":"/var/folders/xx/yy/T/work"}`,
		"runtime": `{"socket":"/run/user/` + `1000/app.sock"}`,
		"uuid":    `{"id":"123e4567-e89b-12d3-a456-426614174000"}`,
		"email":   `{"who":"person@example.org"}`,
		"anthro":  `{"key":"sk-ant-abc"}`,
		"openai":  `{"key":"sk-proj-1234567890abcdef"}`,
		"github":  `{"token":"ghp_abc123"}`,
	}
	for name, text := range dirty {
		if len(scanText(text)) == 0 {
			t.Errorf("%s: the text scan missed %s", name, text)
		}
	}
	clean := `{"cwd":"/home/REDACTED/codex-fixture","id":"uuid-0001"}`
	if got := scanText(clean); len(got) != 0 {
		t.Errorf("the placeholder form was flagged: %v", got)
	}
}
