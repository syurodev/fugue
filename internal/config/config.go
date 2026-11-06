package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Environment variables, in the SYNX_FUGUE_ namespace, plus the XDG runtime
// standard read for the socket directory.
const (
	EnvDataDir   = "SYNX_FUGUE_DATA_DIR"
	EnvLogLevel  = "SYNX_FUGUE_LOG_LEVEL"
	EnvRuntimeDir = "XDG_RUNTIME_DIR"

	dataDirName = "Fugue"
)

// Config is everything the host needs to start.
type Config struct {
	// DataDir holds the database, the lock file and the sockets. It is always
	// absolute and clean.
	DataDir string
	// LogLevel is the minimum level written to the log.
	LogLevel slog.Level
	// RuntimeDir is the base for sockets that do not fit the data directory
	// (host-api §3): XDG_RUNTIME_DIR when set, otherwise a per-user directory
	// fugue creates under the OS temp dir.
	RuntimeDir string
}

// Env looks up environment variables. Production uses ProcessEnv; tests use MapEnv.
type Env interface {
	Lookup(key string) (string, bool)
}

// ProcessEnv reads the real process environment.
type ProcessEnv struct{}

// Lookup returns the variable's value and whether it is set.
func (ProcessEnv) Lookup(key string) (string, bool) { return os.LookupEnv(key) }

// MapEnv is an in-memory environment.
type MapEnv map[string]string

// Lookup returns the variable's value and whether it is set.
func (m MapEnv) Lookup(key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}

// FromProcess loads the configuration from the process environment and the
// operating system's user configuration directory.
func FromProcess() (Config, error) { return Load(ProcessEnv{}, os.UserConfigDir) }

// Load builds a Config. The data directory defaults to <user config dir>/Fugue.
func Load(env Env, userConfigDir func() (string, error)) (Config, error) {
	dir, err := dataDir(env, userConfigDir)
	if err != nil {
		return Config{}, err
	}
	level, err := logLevel(env)
	if err != nil {
		return Config{}, err
	}
	return Config{DataDir: dir, LogLevel: level, RuntimeDir: runtimeDir(env)}, nil
}

// runtimeDir resolves the runtime base for sockets that do not fit the data
// directory. XDG_RUNTIME_DIR is taken as given; otherwise fugue owns a
// per-user directory under the OS temp dir (host-api §3).
func runtimeDir(env Env) string {
	if v, ok := env.Lookup(EnvRuntimeDir); ok && v != "" {
		return filepath.Clean(v)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("fugue-%d", os.Getuid()))
}

func dataDir(env Env, userConfigDir func() (string, error)) (string, error) {
	if v, ok := env.Lookup(EnvDataDir); ok {
		if !filepath.IsAbs(v) {
			return "", errcode.New(errcode.InvalidArgument, EnvDataDir+" must be an absolute path")
		}
		return filepath.Clean(v), nil
	}
	base, err := userConfigDir()
	if err != nil {
		return "", errcode.Wrap(errcode.FailedPrecondition, "no user configuration directory; set "+EnvDataDir, err)
	}
	return filepath.Join(base, dataDirName), nil
}

func logLevel(env Env) (slog.Level, error) {
	v, ok := env.Lookup(EnvLogLevel)
	if !ok || v == "" {
		return slog.LevelInfo, nil
	}
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errcode.New(errcode.InvalidArgument, fmt.Sprintf("%s must be debug, info, warn or error, not %q", EnvLogLevel, v))
	}
}
