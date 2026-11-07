package main

import (
	"testing"

	"github.com/syurodev/fugue/internal/platform/ids"
)

// TestFixtureUsesDeterministicIDs is allowed: the rule reaches non-test files
// only.
func TestFixtureUsesDeterministicIDs(t *testing.T) {
	_ = ids.NewDeterministic("test")
}
