package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

// The data directory is resolved twice in this repository: the host resolves
// it in internal/config, the client in pkg/client. This file may import both,
// so it pins that both sides follow the same rule for every shape the
// environment can have. The rules must move together; a divergent client
// looks for its host in the wrong place.

// envLookup adapts a map to config.Env.
type envLookup map[string]string

func (m envLookup) Lookup(key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}

// errNoConfigDir is the static failure behind a user-config-dir resolver
// that finds nothing; it is wrapped, never constructed, at the call site.
var errNoConfigDir = errors.New("no config dir")

// okUserConfigDir is a resolver that finds the test's config directory.
func okUserConfigDir() (string, error) { return "/cfg", nil }

// resolveBoth resolves the data directory the host's way and the client's
// way, with the same lookup and user-config-dir inputs. Both errors are
// labeled so the assertions can tell the two sides apart.
func resolveBoth(env envLookup, userConfigDir func() (string, error)) (host, cli string, hostErr, cliErr error) {
	cfg, hostErr := config.Load(env, userConfigDir)
	cli, cliErr = client.DataDir(env.Lookup, userConfigDir)
	if hostErr != nil {
		hostErr = fmt.Errorf("the host: %w", hostErr)
	}
	if cliErr != nil {
		cliErr = fmt.Errorf("the client: %w", cliErr)
	}
	return cfg.DataDir, cli, hostErr, cliErr
}

// expectBothResolved asserts that both sides resolved to want.
func expectBothResolved(t *testing.T, want, host, cli string, hostErr, cliErr error) {
	t.Helper()
	if hostErr != nil || cliErr != nil {
		t.Fatalf("expected both to resolve, got hostErr=%v cliErr=%v", hostErr, cliErr)
	}
	if host != want || cli != want {
		t.Errorf("host=%q client=%q, want both %q", host, cli, want)
	}
}

// expectBothFailed asserts that both sides failed, each naming the given
// fragment when one is required. Some failures share only the fact, not the
// message: requireClientMessage decides whether the client's text is checked.
func expectBothFailed(t *testing.T, hostErr, cliErr error, wantFragment string, requireClientMessage bool) {
	t.Helper()
	if hostErr == nil || cliErr == nil {
		t.Fatalf("expected both to fail, got hostErr=%v cliErr=%v", hostErr, cliErr)
	}
	if wantFragment != "" && !strings.Contains(hostErr.Error(), wantFragment) {
		t.Errorf("host error %q lacks %q", hostErr, wantFragment)
	}
	if requireClientMessage && wantFragment != "" && !strings.Contains(cliErr.Error(), wantFragment) {
		t.Errorf("client error %q lacks %q", cliErr, wantFragment)
	}
}

// TestDataDirParity pins that internal/config and pkg/client resolve the same
// data directory, or fail the same way, for: no override, an absolute
// override, a relative override, an empty override and no user config dir.
func TestDataDirParity(t *testing.T) {
	t.Parallel()
	t.Run("no override uses the user config dir", func(t *testing.T) {
		t.Parallel()
		host, cli, hostErr, cliErr := resolveBoth(envLookup{}, okUserConfigDir)
		expectBothResolved(t, filepath.Join("/cfg", "Fugue"), host, cli, hostErr, cliErr)
	})
	t.Run("an absolute override wins on both sides", func(t *testing.T) {
		t.Parallel()
		host, cli, hostErr, cliErr := resolveBoth(
			envLookup{config.EnvDataDir: "/data/fugue"}, okUserConfigDir)
		expectBothResolved(t, "/data/fugue", host, cli, hostErr, cliErr)
	})
	t.Run("a relative override fails on both sides", func(t *testing.T) {
		t.Parallel()
		_, _, hostErr, cliErr := resolveBoth(
			envLookup{config.EnvDataDir: "relative/fugue"}, okUserConfigDir)
		expectBothFailed(t, hostErr, cliErr, "absolute path", true)
	})
	t.Run("an empty override fails on both sides", func(t *testing.T) {
		t.Parallel()
		_, _, hostErr, cliErr := resolveBoth(
			envLookup{config.EnvDataDir: ""}, okUserConfigDir)
		expectBothFailed(t, hostErr, cliErr, "absolute path", true)
	})
	t.Run("no user config dir fails on both sides", func(t *testing.T) {
		t.Parallel()
		_, _, hostErr, cliErr := resolveBoth(envLookup{}, func() (string, error) {
			return "", errNoConfigDir
		})
		// Both fail; the host's message points at the override, the client's
		// wording is its own — only the fact is shared.
		expectBothFailed(t, hostErr, cliErr, "", false)
	})
}

// TestDataDirParityThroughTheProcess pins the same parity with the real
// environment: internal/config reads it through its ProcessEnv and pkg/client
// must reach the same directory when the caller wires the same lookup.
func TestDataDirParityThroughTheProcess(t *testing.T) {
	// t.Setenv forbids t.Parallel; the environment is process-wide.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(config.EnvDataDir, "")
	// An empty value counts as set on both sides, so remove the variable.
	if err := os.Unsetenv(config.EnvDataDir); err != nil {
		t.Fatal(err)
	}
	hostCfg, err := config.FromProcess()
	if err != nil {
		t.Fatalf("the host could not resolve its data dir: %v", err)
	}
	cli, err := client.DataDir(config.ProcessEnv{}.Lookup, os.UserConfigDir)
	if err != nil {
		t.Fatalf("the client could not resolve its data dir: %v", err)
	}
	if hostCfg.DataDir != cli {
		t.Errorf("host=%q client=%q, want both from the same rule", hostCfg.DataDir, cli)
	}
}
