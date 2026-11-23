package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const gitRepoTemplateName = "gitrepo"

// buildGitRepoTemplate creates the one-commit fixture repository once per test
// process. Six git invocations per fixture repository dominated the team
// review tests on macOS, where every git start is slow; a copy of the finished
// directory is one file copy. A missing git leaves no template, and initRepo
// then fails the test that needs it.
func buildGitRepoTemplate(dir string) error {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	repo := filepath.Join(dir, gitRepoTemplateName)
	if mkErr := os.MkdirAll(repo, 0o700); mkErr != nil {
		return fmt.Errorf("create git repo template dir: %w", mkErr)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return fmt.Errorf("open git repo template dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			return
		}
	}()
	if writeErr := root.WriteFile("README", []byte("fixture\n"), 0o600); writeErr != nil {
		return fmt.Errorf("write git repo template README: %w", writeErr)
	}
	const gitConfig = "config"
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{gitConfig, "user.name", "Fugue Test"},
		{gitConfig, "user.email", "fugue@example.invalid"},
		{gitConfig, "commit.gpgsign", "false"},
		{"add", "."},
		{"commit", "-q", "-m", "init"},
	} {
		cmd := exec.CommandContext(context.Background(), "git")
		cmd.Path = gitPath
		cmd.Args = append([]string{"git"}, args...)
		cmd.Dir = repo
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("git %v in template: %w: %s", args, runErr, out)
		}
	}
	return nil
}

// copyGitRepoTemplate fills the empty directory dir with the template repository.
func copyGitRepoTemplate(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(appTemplateDir(os.Getpid()), gitRepoTemplateName)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("git repo template unavailable (is git installed?): %v", err)
	}
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
}
