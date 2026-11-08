package process

import (
	"fmt"
	"strconv"
	"strings"
)

// procEntry holds the parent, process group, and start token of one live OS process.
type procEntry struct {
	pid        int
	ppid       int
	pgid       int
	startToken string
}

// procStatFields holds the parsed fields of a Linux /proc/<pid>/stat line.
type procStatFields struct {
	pid       int
	ppid      int
	pgid      int
	startTime uint64
}

// parseProcStat parses a /proc/<pid>/stat line. Because the process comm field
// (field 2, enclosed in parentheses) may contain spaces, newlines, or nested
// parentheses, parsing begins after the final ')' on the line.
//
// Format: pid (comm) state ppid pgrp session tty_nr tpgid flags minflt cminflt
// majflt cmajflt utime stime cutime cstime priority nice num_threads itrealvalue
// starttime ...
// After ')': field[0]=state, field[1]=ppid, field[2]=pgrp, ..., field[19]=starttime.
func parseProcStat(line, bootID string) (procEntry, error) {
	parsed, err := parseProcStatRaw(line)
	if err != nil {
		return procEntry{}, err
	}
	b := strings.TrimSpace(bootID)
	if b == "" {
		return procEntry{}, fmt.Errorf("%w: empty boot_id", ErrIdentityMismatch)
	}
	return procEntry{
		pid:        parsed.pid,
		ppid:       parsed.ppid,
		pgid:       parsed.pgid,
		startToken: formatLinuxStartToken(b, parsed.startTime),
	}, nil
}

func formatLinuxStartToken(bootID string, startTime uint64) string {
	return bootID + ":" + strconv.FormatUint(startTime, 10)
}

func parseProcStatRaw(line string) (procStatFields, error) {
	pid, rest, err := splitStatPidAndTail(line)
	if err != nil {
		return procStatFields{}, err
	}
	return parseStatTailFields(pid, rest)
}

func splitStatPidAndTail(line string) (int, []string, error) {
	openIdx := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if openIdx <= 0 || closeIdx <= openIdx || closeIdx+2 >= len(line) {
		return 0, nil, fmt.Errorf("%w: malformed stat line", ErrIdentityMismatch)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(line[:openIdx]))
	if err != nil || pid <= 0 {
		return 0, nil, fmt.Errorf("%w: invalid pid in stat", ErrIdentityMismatch)
	}

	rest := strings.Fields(line[closeIdx+1:])
	// rest[0] is state (field 3), rest[1] is ppid (field 4), rest[2] is pgrp (field 5),
	// rest[19] is starttime (field 22).
	const minRestFields = 20
	if len(rest) < minRestFields || !isLiveProcState(rest[0]) {
		return 0, nil, fmt.Errorf("%w: stat line not live or incomplete", ErrIdentityMismatch)
	}
	return pid, rest, nil
}

func isLiveProcState(state string) bool {
	return len(state) == 1 && state != "Z" && state != "X" && state != "x"
}

func parseStatTailFields(pid int, rest []string) (procStatFields, error) {
	ppid, err := strconv.Atoi(rest[1])
	if err != nil || ppid < 0 {
		return procStatFields{}, fmt.Errorf("%w: invalid ppid in stat", ErrIdentityMismatch)
	}
	pgid, err := strconv.Atoi(rest[2])
	if err != nil || pgid < 0 {
		return procStatFields{}, fmt.Errorf("%w: invalid pgrp in stat", ErrIdentityMismatch)
	}
	startTime, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil || startTime == 0 {
		return procStatFields{}, fmt.Errorf("%w: invalid starttime in stat", ErrIdentityMismatch)
	}

	return procStatFields{
		pid:       pid,
		ppid:      ppid,
		pgid:      pgid,
		startTime: startTime,
	}, nil
}

// parseProcCgroupUnit extracts the systemd scope unit directory from a
// /proc/<pid>/cgroup content string and verifies that the v2 unified hierarchy
// line ("0::/<relpath>") ends in the expected unit name.
func parseProcCgroupRelPath(content, expectedUnit string) (string, bool) {
	if expectedUnit == "" || strings.Contains(expectedUnit, "/") || strings.Contains(expectedUnit, "..") {
		return "", false
	}
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		rel, ok := strings.CutPrefix(line, "0::/")
		if !ok {
			continue
		}
		rel = strings.TrimSuffix(rel, "/")
		if rel == expectedUnit || strings.HasSuffix(rel, "/"+expectedUnit) {
			return rel, true
		}
	}
	return "", false
}

// isCgroupEventsUnpopulated reports whether a cgroup.events file contains
// "populated 0".
func isCgroupEventsUnpopulated(content string) bool {
	for _, rawLine := range strings.Split(content, "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) == 2 && fields[0] == "populated" && fields[1] == "0" {
			return true
		}
	}
	return false
}

// parseCgroupProcs parses a cgroup.procs file into a list of positive PIDs.
func parseCgroupProcs(content string) ([]int, bool) {
	var pids []int
	for _, rawLine := range strings.Split(content, "\n") {
		s := strings.TrimSpace(rawLine)
		if s == "" {
			continue
		}
		pid, err := strconv.Atoi(s)
		if err != nil || pid <= 0 {
			return nil, false
		}
		pids = append(pids, pid)
	}
	return pids, true
}

// extractUserManagerCgroupRel parses /proc/self/cgroup content and returns the
// cgroup v2 relative path of the current user's systemd manager subtree
// (".../user@<uid>.service").
func extractUserManagerCgroupRel(content string, uid int) (string, bool) {
	if uid < 0 {
		return "", false
	}
	userService := "user@" + strconv.Itoa(uid) + ".service"
	userSlice := "user-" + strconv.Itoa(uid) + ".slice"
	hasV2Line := false
	for _, rawLine := range strings.Split(content, "\n") {
		rel, ok := strings.CutPrefix(strings.TrimSpace(rawLine), "0::/")
		if !ok {
			continue
		}
		hasV2Line = true
		if mgrRel, found := matchUserManagerSegments(rel, userService, userSlice); found {
			return mgrRel, true
		}
	}
	if hasV2Line {
		return "user.slice/" + userSlice + "/" + userService, true
	}
	return "", false
}

func matchUserManagerSegments(rel, userService, userSlice string) (string, bool) {
	trimmed := strings.Trim(rel, "/")
	if trimmed == "" {
		return "", false
	}
	parts := strings.Split(trimmed, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", false
		}
	}
	for i, p := range parts {
		if p == userService {
			return strings.Join(parts[:i+1], "/"), true
		}
	}
	for i, p := range parts {
		if p == userSlice {
			return strings.Join(parts[:i+1], "/") + "/" + userService, true
		}
	}
	return "", false
}
