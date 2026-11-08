package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// hostFileName is the discovery file a running host writes into the data
// directory (host-api §3); the lock, not this file, is the liveness authority.
const hostFileName = "host.json"

// errNoSocketPath is the static cause behind a discovery file without a
// socket path; it is wrapped, never constructed, at the call site.
var errNoSocketPath = errors.New("the socket path is missing")

// HostFile is the discovery file's client-side shape. It names the socket and
// the host's versions; a client reads it to find a running host and never
// treats its presence as proof that the host still lives.
type HostFile struct {
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	SocketPath  string    `json:"socket_path"`
	APIVersions []string  `json:"api_versions"`
	HostVersion string    `json:"host_version"`
}

// ReadHostFile reads <dataDir>/host.json. A missing file or a missing data
// directory is ErrHostNotRunning: no host has written discovery here. A
// malformed file is a different error — a half-written file should not be
// mistaken for a stopped host.
func ReadHostFile(dataDir string) (HostFile, error) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return HostFile{}, fmt.Errorf("open the data directory %s: %w: %w", dataDir, ErrHostNotRunning, err)
	}
	defer func() {
		// Nothing can act on a close error while only reading discovery.
		if cerr := root.Close(); cerr != nil {
			return
		}
	}()
	data, err := root.ReadFile(hostFileName)
	if errors.Is(err, fs.ErrNotExist) {
		return HostFile{}, fmt.Errorf("read %s in %s: %w", hostFileName, dataDir, ErrHostNotRunning)
	}
	if err != nil {
		return HostFile{}, fmt.Errorf("read %s in %s: %w", hostFileName, dataDir, err)
	}
	var hf HostFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return HostFile{}, fmt.Errorf("parse %s in %s: %w", hostFileName, dataDir, err)
	}
	if hf.SocketPath == "" {
		return HostFile{}, fmt.Errorf("parse %s in %s: %w", hostFileName, dataDir, errNoSocketPath)
	}
	return hf, nil
}

// EnvDataDir is the environment variable that overrides the data directory.
// It is the same variable the host's internal/config reads; this package
// cannot import it, so the name is pinned here and a parity test in cmd/fugue
// asserts both sides resolve the same directory.
const EnvDataDir = "SYNX_FUGUE_DATA_DIR"

// dataDirName is the data directory's name under the user config dir, the
// host's default too (roadmap §0).
const dataDirName = "Fugue"

// DataDir resolves the data directory a client looks for host.json in, with
// the same rule the host applies (internal/config): the SYNX_FUGUE_DATA_DIR
// override when set — it must be absolute, or the typed ErrDataDirNotAbsolute
// is returned — else <user config dir>/Fugue. The environment lookup and the
// user-config-dir resolver come in as parameters, so nothing here reads the
// process environment implicitly; a caller wires os.LookupEnv and
// os.UserConfigDir (or test doubles).
func DataDir(lookup func(key string) (string, bool), userConfigDir func() (string, error)) (string, error) {
	if v, ok := lookup(EnvDataDir); ok {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%w: %s must be an absolute path", ErrDataDirNotAbsolute, EnvDataDir)
		}
		return filepath.Clean(v), nil
	}
	base, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("no user configuration directory; set %s: %w", EnvDataDir, err)
	}
	return filepath.Join(base, dataDirName), nil
}
