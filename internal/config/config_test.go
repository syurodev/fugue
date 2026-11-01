package config_test

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var errNoHome = errors.New("neither $XDG_CONFIG_HOME nor $HOME is defined")

func userDir(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()
	base := filepath.Join(string(filepath.Separator), "home", "ana", ".config")
	got, err := config.Load(config.MapEnv{}, userDir(base))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Config{DataDir: filepath.Join(base, "Fugue"), LogLevel: slog.LevelInfo}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Config mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(string(filepath.Separator), "var", "lib", "fugue")
	env := config.MapEnv{config.EnvDataDir: dir + "/./", config.EnvLogLevel: "DEBUG"}
	got, err := config.Load(env, userDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != dir || got.LogLevel != slog.LevelDebug {
		t.Fatalf("Config = %+v, want data dir %s and debug level", got, dir)
	}
}

func TestLoadLogLevels(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "Error": slog.LevelError, "": slog.LevelInfo} {
		got, err := config.Load(config.MapEnv{config.EnvLogLevel: in}, userDir(t.TempDir()))
		if err != nil || got.LogLevel != want {
			t.Errorf("level %q: %v, %v; want %v", in, got.LogLevel, err, want)
		}
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		env      config.MapEnv
		userDir  func() (string, error)
		wantCode errcode.Code
		wantText string
	}{
		"relative data dir": {config.MapEnv{config.EnvDataDir: "relative/dir"}, userDir("/x"), errcode.InvalidArgument, "absolute"},
		"unknown level":     {config.MapEnv{config.EnvLogLevel: "chatty"}, userDir("/x"), errcode.InvalidArgument, "debug, info, warn or error"},
		"no user dir":       {config.MapEnv{}, func() (string, error) { return "", errNoHome }, errcode.FailedPrecondition, config.EnvDataDir},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(c.env, c.userDir)
			if errcode.CodeOf(err) != c.wantCode || err == nil || !strings.Contains(err.Error(), c.wantText) {
				t.Fatalf("Load = %v, want code %s mentioning %q", err, c.wantCode, c.wantText)
			}
		})
	}
	_, err := config.Load(config.MapEnv{}, func() (string, error) { return "", errNoHome })
	if !errors.Is(err, errNoHome) {
		t.Fatalf("the cause must stay reachable, got %v", err)
	}
}

func TestProcessEnv(t *testing.T) {
	t.Parallel()
	var env config.Env = config.ProcessEnv{}
	if _, ok := env.Lookup("PATH"); !ok {
		t.Error("PATH should be set in a test process")
	}
	if v, ok := env.Lookup("SYNX_FUGUE_SURELY_NOT_SET_BY_ANYONE"); ok || v != "" {
		t.Errorf("unset variable reported as set: %q", v)
	}
}

// FromProcess depends on the machine: it either finds a directory or says why not.
func TestFromProcessFindsADataDirectoryOrExplainsWhy(t *testing.T) {
	t.Parallel()
	got, err := config.FromProcess()
	if err != nil {
		if code := errcode.CodeOf(err); code != errcode.FailedPrecondition && code != errcode.InvalidArgument {
			t.Fatalf("FromProcess = %v, want a configuration error", err)
		}
		return
	}
	if !filepath.IsAbs(got.DataDir) {
		t.Fatalf("DataDir %q is not absolute", got.DataDir)
	}
}
