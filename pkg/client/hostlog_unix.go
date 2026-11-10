//go:build linux || darwin

package client

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func privateInfo(info fs.FileInfo, dir bool) error {
	if err := trustPrivate(info, dir); err != nil {
		return err
	}
	expected := fs.FileMode(0o600)
	if dir {
		expected = 0o700
	}
	if info.Mode().Perm() != expected {
		return ErrUntrustedHost
	}
	return nil
}

// OpenHostLog creates a private log for one launch. With bounded host output,
// up to five logs retain approximately 5 MiB in total.
func OpenHostLog(dataDir string) (*os.File, error) {
	root, err := backgroundRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer closeBestEffort(root)
	if err = root.Mkdir("logs", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logs, err := root.OpenRoot("logs")
	if err != nil {
		return nil, fmt.Errorf("open log directory: %w", err)
	}
	defer closeBestEffort(logs)
	info, err := logs.Stat(".")
	if err == nil {
		err = privateInfo(info, true)
	}
	if err != nil {
		return nil, fmt.Errorf("verify log directory: %w", err)
	}
	return openCheckedLog(logs)
}

const (
	maxHostLogs        = 5
	maxHostLogAttempts = 1024
)

var errHostLogExhausted = errors.New("host log sequence or retry limit exhausted")

type hostLogEntry struct {
	name   string
	number uint64
}

func hostLogs(logs *os.Root) ([]hostLogEntry, error) {
	dir, err := logs.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open log directory for listing: %w", err)
	}
	defer closeBestEffort(dir)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("list host logs: %w", err)
	}
	var result []hostLogEntry
	for _, entry := range entries {
		name := entry.Name()
		digits, ok := hostLogDigits(name)
		if !ok {
			continue
		}
		info, statErr := logs.Lstat(name)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		number, parseErr := strconv.ParseUint(digits, 10, 64)
		if parseErr != nil {
			continue
		}
		result = append(result, hostLogEntry{name: name, number: number})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].number == result[j].number {
			return result[i].name > result[j].name
		}
		return result[i].number > result[j].number
	})
	return result, nil
}

func hostLogDigits(name string) (string, bool) {
	digits, ok := strings.CutPrefix(name, "host-")
	if !ok {
		return "", false
	}
	digits, ok = strings.CutSuffix(digits, ".log")
	if !ok || digits == "" {
		return "", false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return digits, true
}

func openCheckedLog(logs *os.Root) (*os.File, error) {
	// Serialize allocation and pruning so a concurrent launch cannot reuse a
	// sequence after another launch has pruned it.
	lock, err := logs.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open host log lock: %w", err)
	}
	defer closeBestEffort(lock)
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock host log directory: %w", err)
	}
	entries, err := hostLogs(logs)
	if err != nil {
		return nil, err
	}
	var number uint64
	if len(entries) > 0 {
		number = entries[0].number
	}
	for range maxHostLogAttempts {
		if number == ^uint64(0) {
			break
		}
		number++
		name := "host-" + strconv.FormatUint(number, 10) + ".log"
		f, openErr := logs.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(openErr, fs.ErrExist) {
			continue
		}
		if openErr != nil {
			return nil, fmt.Errorf("create host log: %w", openErr)
		}
		info, statErr := f.Stat()
		if statErr == nil {
			statErr = privateInfo(info, false)
		}
		if statErr != nil {
			closeBestEffort(f)
			return nil, fmt.Errorf("verify host log: %w", statErr)
		}
		pruneHostLogs(logs)
		return f, nil
	}
	return nil, fmt.Errorf("create host log: %w", errHostLogExhausted)
}

func pruneHostLogs(logs *os.Root) {
	entries, err := hostLogs(logs)
	if err != nil {
		slog.Warn("list host logs for pruning", "error", err)
		return
	}
	if len(entries) <= maxHostLogs {
		return
	}
	for _, entry := range entries[maxHostLogs:] {
		// On Unix, removing a file still being written by an older sink is safe:
		// the sink retains its inode.
		if err := logs.Remove(entry.name); err != nil {
			slog.Warn("remove old host log", "error", err)
		}
	}
}
