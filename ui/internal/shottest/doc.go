// Package shottest compares a headless render with its committed golden PNG
// in testdata/shots. Before it, the shot tests rendered scenes and checked
// only that the output was not blank, so goldens drifted from the code
// without any test failing. Compare is the single place that decides whether
// a render still matches; the *_regen_test.go files (build tag regenerate)
// stay the only way to rewrite a golden.
//
// Owner: FUG-36. Allowed callers: ui tests.
package shottest
