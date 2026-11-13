package shellrules

import (
	"testing"

	"go.uber.org/goleak"
)

const (
	testWorkspaceRoot = "/workspace/project"
	testCmdGoTest     = "go test"
	testCmdGoTestAll  = "go test ./..."
	testCmdMake       = "make"
	testCmdGit        = "git"
	testCmdEcho       = "echo"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
