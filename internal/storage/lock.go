package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	// LockFileName is the data-directory lock file name (storage-security §9).
	LockFileName = "fugue.lock"
)

// ErrDataDirLocked is returned when another process (or handle) already holds
// the exclusive lock on <dataDir>/fugue.lock.
var ErrDataDirLocked = errcode.New(errcode.FailedPrecondition, "data directory is locked by another host process")

// LockOptions configures AcquireLockWith.
type LockOptions struct {
	// CheckFilesystem validates that the canonical data directory resides on a
	// supported local filesystem. When nil, the OS default check is used.
	CheckFilesystem func(canonicalDir string) error
}

// DirLock holds the OS-backed exclusive lock on <dataDir>/fugue.lock for the
// lifetime of the host process.
type DirLock struct {
	mu           sync.RWMutex
	canonicalDir string
	lockPath     string
	file         *os.File
	held         bool
}

// LockPath returns the path to fugue.lock inside dataDir.
func LockPath(dataDir string) string {
	return filepath.Join(dataDir, LockFileName)
}

// AcquireLock canonicalizes dataDir, rejects network filesystems and acquires
// an exclusive non-blocking OS lock on <dataDir>/fugue.lock with mode 0600.
func AcquireLock(dataDir string) (*DirLock, error) {
	return AcquireLockWith(dataDir, nil)
}

// AcquireLockWith is AcquireLock with optional overrides for testing.
func AcquireLockWith(dataDir string, opts *LockOptions) (*DirLock, error) {
	canonicalDir, err := prepareCanonicalDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	checkFS := checkLocalFilesystem
	if opts != nil && opts.CheckFilesystem != nil {
		checkFS = opts.CheckFilesystem
	}
	if fsErr := checkFS(canonicalDir); fsErr != nil {
		return nil, fsErr
	}
	root, err := os.OpenRoot(canonicalDir)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "open data directory root "+canonicalDir, err)
	}
	defer closeQuietly(root)

	lockPath := filepath.Join(canonicalDir, LockFileName)
	file, err := root.OpenFile(LockFileName, os.O_RDWR|os.O_CREATE, dbFilePerm)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "open lock file "+lockPath, err)
	}
	if chmodErr := root.Chmod(LockFileName, dbFilePerm); chmodErr != nil {
		closeQuietly(file)
		return nil, errcode.Wrap(errcode.Internal, "restrict lock file permissions "+lockPath, chmodErr)
	}
	if lockErr := lockFileExclusive(file); lockErr != nil {
		closeQuietly(file)
		if errors.Is(lockErr, ErrDataDirLocked) {
			return nil, errcode.Wrap(
				errcode.FailedPrecondition,
				fmt.Sprintf("data directory %s is already locked (%s)", canonicalDir, lockPath),
				ErrDataDirLocked,
			)
		}
		return nil, errcode.Wrap(errcode.Internal, "acquire lock "+lockPath, lockErr)
	}
	return &DirLock{
		mu:           sync.RWMutex{},
		canonicalDir: canonicalDir,
		lockPath:     lockPath,
		file:         file,
		held:         true,
	}, nil
}

func prepareCanonicalDataDir(dataDir string) (string, error) {
	trimmed := strings.TrimSpace(dataDir)
	if trimmed == "" {
		return "", errcode.New(errcode.InvalidArgument, "data directory is required")
	}
	absDir, err := filepath.Abs(trimmed)
	if err != nil {
		return "", errcode.Wrap(errcode.InvalidArgument, "resolve absolute data directory", err)
	}
	if mkdirErr := os.MkdirAll(absDir, dataDirPerm); mkdirErr != nil {
		return "", errcode.Wrap(errcode.Internal, "create data directory "+absDir, mkdirErr)
	}
	canonicalDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "evaluate data directory symlinks "+absDir, err)
	}
	return filepath.Clean(canonicalDir), nil
}

// DataDir returns the canonicalized data directory path protected by the lock.
func (l *DirLock) DataDir() string {
	if l == nil {
		return ""
	}
	return l.canonicalDir
}

// Path returns the canonical path to fugue.lock.
func (l *DirLock) Path() string {
	if l == nil {
		return ""
	}
	return l.lockPath
}

// Held reports whether the lock is currently held by this DirLock instance.
func (l *DirLock) Held() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.held
}

// Release unlocks and closes the underlying file handle without unlinking
// fugue.lock (event-storage-recovery §3). Calling Release more than once is a
// no-op.
func (l *DirLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return nil
	}
	l.held = false
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return errcode.Wrap(errcode.Internal, "unlock "+l.lockPath, unlockErr)
	}
	if closeErr != nil {
		return errcode.Wrap(errcode.Internal, "close lock file "+l.lockPath, closeErr)
	}
	return nil
}

func isNetworkFSTypeName(fsType string) bool {
	switch strings.ToLower(strings.TrimSpace(fsType)) {
	case "nfs", "nfs4", "nfsd", "smb", "smbfs", "cifs", "smb2", "afp", "afpfs", "webdav", "ceph", "coda", "lustre", "gpfs", "fhgfs":
		return true
	default:
		return false
	}
}

func isLinuxNetworkFSMagic(magic int64) (string, bool) {
	type entry struct {
		magic int64
		name  string
	}
	table := [...]entry{
		{magic: 0x6969, name: "nfs"},
		{magic: 0x6e667364, name: "nfsd"},
		{magic: 0x517b, name: "smb"},
		{magic: 0xff534d42, name: "cifs"},
		{magic: 0xfe534d42, name: "smb2"},
		{magic: 0x5346414f, name: "afp"},
		{magic: 0x00c36400, name: "ceph"},
		{magic: 0x47504653, name: "gpfs"},
		{magic: 0x0bd00bd0, name: "lustre"},
		{magic: 0x73757245, name: "coda"},
		{magic: 0x19830326, name: "fhgfs"},
	}
	for _, e := range table {
		if e.magic == magic {
			return e.name, true
		}
	}
	return "", false
}
