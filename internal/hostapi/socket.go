package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	// SocketFileName is the Host API socket inside the data directory
	// (host-api §3).
	SocketFileName = "fugue.sock"

	// runtimeSocketDirName is the directory fugue creates under the runtime
	// base when the data-directory socket path does not fit sun_path.
	runtimeSocketDirName = "fugue"

	// defaultMaxPath is a conservative sun_path budget; the limits are 104
	// bytes on macOS and 108 on Linux, and the trailing NUL counts.
	defaultMaxPath = 100

	// probeTimeout bounds the live-host probe of a leftover socket. A unix
	// connect answers immediately; the budget only covers scheduling.
	probeTimeout = 200 * time.Millisecond

	dirPerm         fs.FileMode = 0o700
	socketFilePerm  fs.FileMode = 0o600
	runtimeBaseName             = "fugue-%d"
)

// DirStat is what the trust check needs to know about one open directory.
// UIDKnown is false when the platform cannot report the owner; the check then
// fails closed.
type DirStat struct {
	Mode     fs.FileMode
	UID      int64
	UIDKnown bool
}

// Dir is one open directory descriptor. The trust check opens, stats and
// chmods the same descriptor so nothing can replace the directory between the
// check and the chmod.
type Dir interface {
	Stat() (DirStat, error)
	Chmod(mode fs.FileMode) error
	Close() error
}

// DirOps opens directories as descriptors.
type DirOps interface {
	Open(path string) (Dir, error)
}

// SocketOptions configures ListenSocket.
type SocketOptions struct {
	// DataDir is the canonical data directory; it holds the socket when its
	// path fits sun_path.
	DataDir string
	// RuntimeDir is the runtime base from config; it holds the socket when the
	// data-directory path does not fit sun_path (host-api §3).
	RuntimeDir string
	// MaxPath caps the socket path length; 0 uses the conservative default.
	MaxPath int
	// Dirs is the directory seam; nil uses the platform implementation.
	Dirs DirOps
}

// Socket is the bound Host API listener and the path that went into host.json.
type Socket struct {
	Path     string
	Listener net.Listener
}

// ListenSocket resolves the socket path, trust-checks every directory on the
// way, replaces a crashed host's leftover socket after a live-host probe and
// binds the listener with owner-only permissions. The socket directory is
// 0700 before the socket exists and the socket is 0600 right after bind, so
// there is no window in which another user can reach it.
func ListenSocket(ctx context.Context, opts SocketOptions) (*Socket, error) {
	dir, name, err := resolveSocketPath(opts)
	if err != nil {
		return nil, err
	}
	dirs := opts.Dirs
	if dirs == nil {
		dirs = osDirs{}
	}
	if dir != opts.DataDir {
		if terr := trustDir(dirs, opts.RuntimeDir, isManagedRuntimeBase(opts.RuntimeDir)); terr != nil {
			return nil, terr
		}
	}
	if terr := trustDir(dirs, dir, true); terr != nil {
		return nil, terr
	}
	socketPath := filepath.Join(dir, name)
	if serr := clearSocketPath(ctx, socketPath); serr != nil {
		return nil, serr
	}
	ln, err := new(net.ListenConfig).Listen(ctx, "unix", socketPath)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "bind host socket "+socketPath, err)
	}
	if err := os.Chmod(socketPath, socketFilePerm); err != nil {
		closeQuietly(ln)
		return nil, errcode.Wrap(errcode.Internal, "restrict socket permissions "+socketPath, err)
	}
	return &Socket{Path: socketPath, Listener: ln}, nil
}

// resolveSocketPath picks the socket directory and name: the data directory
// while the path fits sun_path, otherwise the runtime base's fugue directory
// with a name hashed from the canonical data directory (host-api §3).
func resolveSocketPath(opts SocketOptions) (dir, name string, err error) {
	if opts.DataDir == "" {
		return "", "", errcode.New(errcode.InvalidArgument, "data directory is required")
	}
	max := opts.MaxPath
	if max == 0 {
		max = defaultMaxPath
	}
	if len(filepath.Join(opts.DataDir, SocketFileName)) < max {
		return opts.DataDir, SocketFileName, nil
	}
	if opts.RuntimeDir == "" {
		return "", "", errcode.New(errcode.InvalidArgument,
			"the data-directory socket path exceeds sun_path and "+config.EnvRuntimeDir+" is not set")
	}
	return filepath.Join(opts.RuntimeDir, runtimeSocketDirName), hashedSocketName(opts.DataDir), nil
}

// trustDir walks one directory level on an open descriptor: not a symlink
// (O_NOFOLLOW makes the open fail), owned by this process user with an owner
// the platform can report, and — when fugue manages the directory — mode
// exactly 0700, created when absent and tightened when it exists. A directory
// fugue does not create, such as XDG_RUNTIME_DIR, must already exist and keeps
// its mode (host-api §3).
func trustDir(dirs DirOps, path string, managed bool) error {
	d, err := openTrustedDir(dirs, path, managed)
	if err != nil {
		return err
	}
	defer closeQuietly(d)
	return checkTrustedDir(d, path, managed)
}

// openTrustedDir returns the open descriptor of path, creating a managed
// directory 0700 when it is absent.
func openTrustedDir(dirs DirOps, path string, managed bool) (Dir, error) {
	d, err := dirs.Open(path)
	if err == nil {
		return d, nil
	}
	if !managed || !errors.Is(err, fs.ErrNotExist) {
		return nil, untrusted(fmt.Sprintf("open socket directory %s: %v", path, err))
	}
	if mkdirErr := os.Mkdir(path, dirPerm); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
		return nil, errcode.Wrap(errcode.Internal, "create socket directory "+path, mkdirErr)
	}
	d, err = dirs.Open(path)
	if err != nil {
		return nil, untrusted(fmt.Sprintf("open created socket directory %s: %v", path, err))
	}
	return d, nil
}

// checkTrustedDir inspects the open descriptor and tightens the mode of a
// managed directory fugue owns. Nothing runs on a path twice.
func checkTrustedDir(d Dir, path string, managed bool) error {
	st, err := d.Stat()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "stat socket directory "+path, err)
	}
	if st.Mode&fs.ModeDir == 0 {
		return untrusted(fmt.Sprintf("socket directory %s is not a directory (%v)", path, st.Mode))
	}
	if !st.UIDKnown || st.UID != int64(os.Getuid()) {
		return untrusted(fmt.Sprintf(
			"socket directory %s is not owned by this process user (uid %d, known %v)",
			path, st.UID, st.UIDKnown))
	}
	if managed && st.Mode.Perm() != dirPerm {
		if err := d.Chmod(dirPerm); err != nil {
			return errcode.Wrap(errcode.Internal, "restrict socket directory "+path, err)
		}
	}
	return nil
}

func untrusted(msg string) error {
	return errcode.Wrap(errcode.PermissionDenied, msg, ErrSocketDirectoryUntrusted)
}

// clearSocketPath removes a crashed host's leftover socket after proving that
// nothing answers it, and refuses to touch anything that is not a socket. The
// data-directory lock decides who is alive; the probe is defence in depth so
// a live host's socket is never unlinked even if the lock is wrong.
func clearSocketPath(ctx context.Context, path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errcode.Wrap(errcode.Internal, "stat socket path "+path, err)
	}
	if fi.Mode()&fs.ModeSocket == 0 {
		return errcode.Wrap(errcode.FailedPrecondition,
			fmt.Sprintf("socket path %s exists and is %v, not a socket", path, fi.Mode()), ErrSocketPathNotSocket)
	}
	conn, perr := (&net.Dialer{Timeout: probeTimeout}).DialContext(ctx, "unix", path)
	if perr == nil {
		closeQuietly(conn)
		return errcode.Wrap(errcode.FailedPrecondition,
			fmt.Sprintf("socket path %s answers a probe", path), ErrSocketInUse)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errcode.Wrap(errcode.Internal, "remove stale socket "+path, err)
	}
	return nil
}

// isManagedRuntimeBase reports whether the runtime base is the per-user
// directory fugue creates (config's temp fallback, named fugue-<uid>) rather
// than a directory given to it, such as XDG_RUNTIME_DIR, whose mode fugue
// leaves alone.
func isManagedRuntimeBase(base string) bool {
	return filepath.Base(base) == fmt.Sprintf(runtimeBaseName, os.Getuid())
}

// hashedSocketName keeps runtime sockets for different data directories apart
// without putting a long data-directory path into the runtime directory.
func hashedSocketName(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	return "fugue-" + hex.EncodeToString(sum[:])[:12] + ".sock"
}
