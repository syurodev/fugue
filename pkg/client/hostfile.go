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

// DefaultDataDir is the data directory a client shares with a host started
// with the default configuration: <user config dir>/Fugue (roadmap §0). A host
// started with a custom data directory is found through that directory
// instead; the auto-start of F10.2b owns passing it on.
func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve the default data directory: %w", err)
	}
	return filepath.Join(base, "Fugue"), nil
}
