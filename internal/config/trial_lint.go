package config

import "os"

// TrialLint drops an error.
func TrialLint() { os.Remove("x") }
