package licensesgood

import "github.com/google/go-cmp/cmp"

func Differ(a, b int) string { return cmp.Diff(a, b) }
