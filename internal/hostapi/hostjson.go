package hostapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	hostFileName = "host.json"
	hostFilePerm = 0o600
)

// HostFile is the discovery record of a running host (host-api §3). It holds
// no secret, and it is discovery only: the data-directory lock decides who is
// alive, and a client trusts the file only after a successful Hello over the
// socket it names — a rule F7.1 implements.
type HostFile struct {
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	SocketPath  string    `json:"socket_path"`
	APIVersions []string  `json:"api_versions"`
	HostVersion string    `json:"host_version"`
}

// HostFilePath returns the discovery file's path in the data directory.
func HostFilePath(dataDir string) string {
	return filepath.Join(dataDir, hostFileName)
}

// WriteHostFile publishes hf atomically: a fresh owner-only temp file is
// written in the data directory and renamed over host.json, so a reader never
// sees a torn file. A record left by a crashed host is simply overwritten;
// nothing is read back for host-side decisions.
func WriteHostFile(dataDir string, hf *HostFile) error {
	b, err := json.Marshal(hf)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode host file", err)
	}
	f, err := os.CreateTemp(dataDir, hostFileName+"-*")
	if err != nil {
		return errcode.Wrap(errcode.Internal, "create host file in "+dataDir, err)
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		closeQuietly(f)
		removeQuietly(tmp)
		return errcode.Wrap(errcode.Internal, "write host file", err)
	}
	if err := f.Close(); err != nil {
		removeQuietly(tmp)
		return errcode.Wrap(errcode.Internal, "close host file", err)
	}
	if err := os.Rename(tmp, HostFilePath(dataDir)); err != nil {
		removeQuietly(tmp)
		return errcode.Wrap(errcode.Internal, "publish host file in "+dataDir, err)
	}
	return nil
}

// RemoveHostFile deletes the discovery file; an absent file is not an error.
func RemoveHostFile(dataDir string) error {
	err := os.Remove(HostFilePath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errcode.Wrap(errcode.Internal, "remove host file", err)
	}
	return nil
}

func removeQuietly(path string) {
	if err := os.Remove(path); err != nil {
		return
	}
}
