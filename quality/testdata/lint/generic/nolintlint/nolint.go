// lint:expect nolintlint

package nolintlint

import "os"

func Bare() {
	os.Remove("x") //nolint
}

func NoReason() {
	os.Remove("y") //nolint:errcheck
}

func Unused() int {
	return 1 //nolint:errcheck // nothing to suppress here
}
