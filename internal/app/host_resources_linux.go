//go:build linux

package app

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func hostTotalMemoryBytes() (uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("MemTotal:")) {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 2 {
			break
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal: %w", err)
		}
		return kib * 1024, nil
	}
	return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
}
