//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"runtime"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ErrLandlockSkipped indicates Landlock could not be applied because the kernel
// or LSM does not support it.
var ErrLandlockSkipped = errcode.New(errcode.Unavailable, "landlock skipped")

const (
	landlockFSAccessRO = unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR

	landlockFSAccessRW = landlockFSAccessRO |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE
)

type landlockOps struct {
	probeABI        func() (int, error)
	createRuleset   func(attr *unix.LandlockRulesetAttr, size uintptr, flags uint32) (int, error)
	addPathBeneath  func(rulesetFD int, path string, access uint64) error
	restrictCurrent func(rulesetFD int, flags uint32) error
	closeFD         func(fd int)
}

func defaultLandlockOps() landlockOps {
	return landlockOps{
		probeABI:        landlockProbeABI,
		createRuleset:   landlockCreateRuleset,
		addPathBeneath:  landlockAddPathBeneath,
		restrictCurrent: landlockRestrictSelf,
		closeFD:         closeFDQuietly,
	}
}

// LandlockAvailable reports whether the kernel exposes Landlock syscalls.
func LandlockAvailable() bool {
	return landlockAvailableWith(defaultLandlockOps())
}

func landlockAvailableWith(ops landlockOps) bool {
	abi, err := ops.probeABI()
	if err != nil {
		return false
	}
	return abi > 0
}

// RestrictSelf applies Landlock path-beneath rules for policy paths to the
// calling thread. WorkDir and ReadWritePaths receive read-write access;
// ReadOnlyPaths receive read-only access.
func RestrictSelf(p *Policy) error {
	return restrictSelfWith(p, defaultLandlockOps())
}

func restrictSelfWith(p *Policy, ops landlockOps) error {
	if err := p.Validate(); err != nil {
		return err
	}

	attr := unix.LandlockRulesetAttr{
		Handled_access_fs: landlockFSAccessRW,
	}
	fd, err := ops.createRuleset(&attr, reflect.TypeFor[unix.LandlockRulesetAttr]().Size(), 0)
	if err != nil {
		if landlockSkippedErr(err) {
			return ErrLandlockSkipped
		}
		return fmt.Errorf("landlock create ruleset: %w", err)
	}
	defer ops.closeFD(fd)

	for _, entry := range landlockPolicyPaths(p) {
		if addErr := ops.addPathBeneath(fd, entry.path, entry.access); addErr != nil {
			return fmt.Errorf("landlock add rule %s: %w", entry.path, addErr)
		}
	}

	if err := ops.restrictCurrent(fd, 0); err != nil {
		if landlockSkippedErr(err) {
			return ErrLandlockSkipped
		}
		return fmt.Errorf("landlock restrict self: %w", err)
	}
	return nil
}

type landlockPathEntry struct {
	path   string
	access uint64
}

func landlockPolicyPaths(p *Policy) []landlockPathEntry {
	seen := make(map[string]struct{})
	var out []landlockPathEntry

	add := func(path string, access uint64) {
		trimmed := filepath.Clean(path)
		if path == "" || trimmed == "." {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		out = append(out, landlockPathEntry{path: trimmed, access: access})
	}

	add(p.WorkDir, landlockFSAccessRW)
	for _, path := range p.ReadWritePaths {
		add(path, landlockFSAccessRW)
	}
	for _, path := range p.ReadOnlyPaths {
		add(path, landlockFSAccessRO)
	}
	return out
}

func landlockProbeABI() (int, error) {
	abi, _, errno := unix.RawSyscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return 0, fmt.Errorf("landlock probe abi: %w", errno)
	}
	return int(abi), nil
}

func landlockCreateRuleset(attr *unix.LandlockRulesetAttr, size uintptr, flags uint32) (int, error) {
	var attrAddr uintptr
	if attr != nil {
		attrAddr = reflect.ValueOf(attr).Pointer()
	}
	fd, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		attrAddr,
		size,
		uintptr(flags),
		0,
		0,
		0,
	)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return -1, fmt.Errorf("landlock create ruleset syscall: %w", errno)
	}
	return int(fd), nil
}

func landlockAddPathBeneath(rulesetFD int, path string, access uint64) error {
	parentFD, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open path fd: %w", err)
	}
	defer closeFDQuietly(parentFD)

	if parentFD < 0 || parentFD > math.MaxInt32 {
		return errcode.New(errcode.Internal, "landlock parent fd out of int32 range")
	}
	rule := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(parentFD),
	}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		reflect.ValueOf(&rule).Pointer(),
		0,
		0,
		0,
	)
	runtime.KeepAlive(&rule)
	if errno != 0 {
		return fmt.Errorf("landlock add rule syscall: %w", errno)
	}
	return nil
}

func landlockRestrictSelf(rulesetFD int, flags uint32) error {
	_, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(rulesetFD),
		uintptr(flags),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock restrict self syscall: %w", errno)
	}
	return nil
}

func landlockSkippedErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENOPKG) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EPERM)
}

func closeFDQuietly(fd int) {
	if err := unix.Close(fd); err != nil {
		return
	}
}
