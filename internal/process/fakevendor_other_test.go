//go:build !unix

package process

import "os"

// unixModes is empty: Spawn is a fail-closed stub here, so no fake vendor runs.
func unixModes() map[string]func() {
	return nil
}

func blockForever() {
	os.Exit(3)
}
