package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLiveVerificationRecordsContainNoSecrets(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := scanLiveRecordsDir(filepath.Join("..", "..", "quality", "records"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 0 {
			t.Fatalf("unexpected secret findings in live records:\n%s", strings.Join(got, "\n"))
		}
	})
	t.Run("planted secret", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join("testdata", "live-record-secret", "quality", "records")
		got, err := scanLiveRecordsDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(got, func(v string) bool {
			return strings.Contains(v, "2026-11-13-linux.json") && strings.Contains(v, KindGitHubToken)
		}) {
			t.Fatalf("expected finding in planted secret fixture, got %v", got)
		}
	})
}

func scanLiveRecordsDir(root string) ([]string, error) {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	dirFS := os.DirFS(root)
	var out []string
	err := fs.WalkDir(dirFS, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(d.Name()) != ".json" {
			return nil
		}
		raw, readErr := fs.ReadFile(dirFS, p)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", p, readErr)
		}
		if findings := Find(string(raw)); len(findings) > 0 {
			out = append(out, fmt.Sprintf("%s contains a secret (%s)", p, findings[0].Kind))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	slices.Sort(out)
	return out, nil
}
