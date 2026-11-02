//go:build !linux

package sandbox

func runPlatformChildHelper([]string) (int, bool) {
	return 0, false
}

