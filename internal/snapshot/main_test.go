package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testEngine(t *testing.T, limits Limits) (*Engine, *Repo) {
	t.Helper()
	fakeClock := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	return testEngineWithClock(t, limits, fakeClock)
}

func testEngineWithClock(t *testing.T, limits Limits, clk clock.Clock) (*Engine, *Repo) {
	t.Helper()
	git := Locate(context.Background())
	if !git.OK() {
		t.Fatalf("snapshot: git unavailable: %s", git.Reason)
	}
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("snapshot-test"))
	engine := NewWithDeps(filepath.Join(t.TempDir(), "snapshots"), git, limits, clk, idGen)
	repo, err := engine.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return engine, repo
}

const (
	contentOne   = "one\n"
	contentTwo   = "two\n"
	contentThree = "three\n"
)

func write(t *testing.T, repo *Repo, rel, content string) {
	t.Helper()
	wsRoot, err := os.OpenRoot(repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := wsRoot.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	fsRel := filepath.FromSlash(rel)
	if dir := filepath.Dir(fsRel); dir != "." && dir != "" {
		if err := wsRoot.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := wsRoot.WriteFile(fsRel, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func chmod(t *testing.T, repo *Repo, rel string, mode os.FileMode) {
	t.Helper()
	wsRoot, err := os.OpenRoot(repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := wsRoot.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	if err := wsRoot.Chmod(filepath.FromSlash(rel), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, repo *Repo, rel string) string {
	t.Helper()
	wsRoot, err := os.OpenRoot(repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := wsRoot.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	raw, err := wsRoot.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func snap(t *testing.T, repo *Repo, id, slot string) Snapshot {
	t.Helper()
	out, err := repo.Snapshot(context.Background(), id, slot)
	if err != nil {
		t.Fatalf("snapshot %s/%s: %v", id, slot, err)
	}
	return out
}

func treePaths(t *testing.T, repo *Repo, tree string) []string {
	t.Helper()
	raw, err := repo.git(context.Background(), nil, "ls-tree", "-r", "-z", "--name-only", tree)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	return splitNUL(raw)
}

func contains(list []string, want string) bool {
	return slices.Contains(list, want)
}

func skipReason(snapshot Snapshot, path string) SkipReason {
	for _, skip := range snapshot.Skipped {
		if skip.Path == path {
			return skip.Reason
		}
	}
	return ""
}

func conflictOn(plan *RestorePlan, path string) (Conflict, bool) {
	for _, conflict := range plan.Conflicts {
		if conflict.Path == path {
			return conflict, true
		}
	}
	return Conflict{}, false
}

func hasActionOn(plan *RestorePlan, path string) bool {
	for i := range plan.Actions {
		if plan.Actions[i].Path == path {
			return true
		}
	}
	return false
}

func refExists(t *testing.T, repo *Repo, id, slot string) bool {
	t.Helper()
	raw, err := repo.git(context.Background(), nil, "for-each-ref", "--format=%(refname)", refPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(raw), RefName(id, slot))
}
