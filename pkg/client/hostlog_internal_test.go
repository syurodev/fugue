//go:build linux || darwin

package client

import (
	"os"
	"testing"
)

func TestHostLogClosedRootErrors(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = hostLogs(root); err == nil {
		t.Fatal("listing a closed root succeeded")
	}
	if _, err = openCheckedLog(root); err == nil {
		t.Fatal("creating a log in a closed root succeeded")
	}
	pruneHostLogs(root)
}
