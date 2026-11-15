package agy

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
		var docs [][]byte
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				docs = append(docs, []byte(line))
			}
		}
		return docs
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

func TestAgyFixturesContainNoHostIdentity(t *testing.T) {
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
		scanned++
		raw, readErr := fs.ReadFile(root, path)
		if readErr != nil {
			return fmt.Errorf("read fixture %s: %w", path, readErr)
		}
		for _, finding := range scanFile(path, raw) {
			t.Error(finding)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("expected at least one fixture file under testdata")
	}
}
