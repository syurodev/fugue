package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	dirRoot, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := dirRoot.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	if err := dirRoot.WriteFile("git", []byte("#!/bin/sh\n"+body), 0o750); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "git")
}

func TestShimIsNeverRunWithoutCommandLineTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "it-ran")
	shim := writeScript(t, dir, "touch "+marker+"\necho 'git version 2.50.0'\n")

	got := LocateWith(context.Background(), &LocateOptions{
		GOOS:           goosDarwin,
		ShimPath:       shim,
		ToolchainPaths: []string{filepath.Join(dir, "no-such-toolchain", "git")},
		SearchDirs:     []string{filepath.Join(dir, "empty")},
		LookPath:       func(string) (string, error) { return shim, nil },
	})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the shim was executed")
	}
	if got.OK() {
		t.Fatalf("availability = %+v, want unavailable", got)
	}
	if !strings.Contains(got.Reason, "xcode-select") {
		t.Fatalf("reason = %q, want the xcode-select fix", got.Reason)
	}
}

func TestShimIsUsedWhenCommandLineToolsArePresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	shim := writeScript(t, dir, "echo 'git version 2.39.5 (Apple Git-154)'\n")
	toolchain := writeScript(t, filepath.Join(dir, "toolchain"), "echo real\n")

	got := LocateWith(context.Background(), &LocateOptions{
		GOOS:           goosDarwin,
		ShimPath:       shim,
		ToolchainPaths: []string{toolchain},
		SearchDirs:     []string{filepath.Join(dir, "empty")},
		LookPath:       func(string) (string, error) { return shim, nil },
	})
	if got.Path != shim {
		t.Fatalf("path = %q, want %q (reason %q)", got.Path, shim, got.Reason)
	}
	if got.Version != "2.39.5" {
		t.Fatalf("version = %q, want 2.39.5 parsed out of Apple's banner", got.Version)
	}
}

func TestTooOldGitSaysSo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "bin"), "echo 'git version 2.10.0'\n")

	got := LocateWith(context.Background(), &LocateOptions{
		GOOS:       "linux",
		SearchDirs: []string{filepath.Join(dir, "bin")},
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
	})
	if got.OK() {
		t.Fatalf("availability = %+v, want unavailable", got)
	}
	if !strings.Contains(got.Reason, "2.10.0") || !strings.Contains(got.Reason, MinGitVersion) {
		t.Fatalf("reason = %q, want both versions named", got.Reason)
	}
}

func TestCompareGitVersionsIgnoresVendorBanners(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"2.39.5 (Apple Git-154)", MinGitVersion, 1},
		{"2.26.0", MinGitVersion, 0},
		{"2.9.9", MinGitVersion, -1},
		{"10.0.0", "9.99.99", 1},
	}
	for _, tc := range cases {
		if got := compareGitVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
