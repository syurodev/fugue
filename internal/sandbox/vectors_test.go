package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func errProfileRejected() error {
	return ErrUnavailable
}

func errNoResolvSymlink() error {
	return os.ErrNotExist
}

func TestSeatbeltProfileMatchesTheOriginalVectors(t *testing.T) {
	t.Parallel()

	p1 := Policy{
		WorkDir:          "/Users/alice/workspace/app",
		ReadOnlyPaths:    []string{"/opt/tools"},
		ReadWritePaths:   []string{"/Users/alice/workspace/shared"},
		DenyReadPaths:    []string{"/Users/alice/workspace/app/.env"},
		DenyWritePaths:   []string{"/Users/alice/workspace/app/AGENTS.md"},
		AllowUnixSockets: []string{"/private/tmp/mcp.sock"},
		Env:              map[string]string{testEnvHome: "/Users/alice"},
		Network:          NetworkNone,
	}
	want1, err := os.ReadFile("testdata/seatbelt-network-none.sbpl")
	if err != nil {
		t.Fatal(err)
	}
	got1 := buildSeatbeltProfileForHost(&p1, "/Users/alice", true)
	if diff := cmp.Diff(string(want1), got1); diff != "" {
		t.Fatalf("seatbelt-network-none profile differs from original (-want +got):\n%s", diff)
	}

	p2 := Policy{
		SessionTempDir:         testSessionTempDir,
		WorkDir:                "/Users/alice/workspace/app",
		ReadPreferenceDomains:  []string{"com.openai.codex"},
		ListenLoopbackPorts:    []int{1455},
		AllowLoopbackListeners: true,
		ReadAncestors:          true,
		AllowKeychain:          true,
		PrivateHome:            boolPtr(false),
		Env:                    map[string]string{testEnvHome: "/Users/alice"},
		Network:                NetworkHost,
	}
	want2, err := os.ReadFile("testdata/seatbelt-network-host.sbpl")
	if err != nil {
		t.Fatal(err)
	}
	got2 := buildSeatbeltProfileForHost(&p2, "/Users/alice", true)
	if diff := cmp.Diff(string(want2), got2); diff != "" {
		t.Fatalf("seatbelt-network-host profile differs from original (-want +got):\n%s", diff)
	}
}

func TestBwrapArgsMatchesTheOriginalVector(t *testing.T) {
	t.Parallel()

	const root = "/tmp/dk-bwrap-vec"
	home := root + "/home"
	workDir := home + "/proj"
	sshDir := home + "/.ssh"
	codexDir := home + "/.config/codex"
	rulesDir := codexDir + "/rules"
	roDir := root + "/tools"
	sockPath := home + "/.config/mcp.sock"
	secretFile := workDir + "/.env"

	env := vectorBwrapEnv(home, []string{workDir, sshDir, codexDir, rulesDir, roDir}, []string{sockPath, secretFile})
	got := buildBwrapArgsWith(&Policy{
		WorkDir:          workDir,
		ReadOnlyPaths:    []string{roDir},
		ReadWritePaths:   []string{codexDir},
		DenyReadPaths:    []string{secretFile},
		DenyWritePaths:   []string{rulesDir},
		AllowUnixSockets: []string{sockPath},
		Env:              map[string]string{testEnvHome: home, "CI": "1"},
		Network:          NetworkNone,
	}, Cmd{Path: testBinSh, Args: []string{"-c", "echo ok"}}, env)

	rawWant, err := os.ReadFile("testdata/bwrap-args.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if unmarshalErr := json.Unmarshal(rawWant, &want); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("bwrap args differ from original (-want +got):\n%s", diff)
	}
}

func vectorBwrapEnv(home string, extraDirs, extraFiles []string) bwrapEnv {
	dirs := map[string]bool{
		"/usr":   true,
		"/bin":   true,
		"/lib":   true,
		"/lib64": true,
		"/etc":   true,
	}
	for _, d := range extraDirs {
		dirs[d] = true
	}
	files := make(map[string]bool, len(extraFiles))
	for _, f := range extraFiles {
		files[f] = true
	}
	return bwrapEnv{
		hostHome:       home,
		resolvConfPath: defaultResolvConfPath,
		stat: func(path string) bwrapPathKind {
			if dirs[path] {
				return bwrapPathDir
			}
			if files[path] {
				return bwrapPathFile
			}
			return bwrapPathMissing
		},
		evalSymlinks: func(string) (string, error) {
			return "", errNoResolvSymlink()
		},
	}
}

type fakeProfileRunner struct {
	err      error
	outcomes []error
	calls    int
	profile  string
}

func (r *fakeProfileRunner) Run(ctx context.Context, _ *Policy, profile string) error {
	r.calls++
	r.profile = profile
	if _, ok := ctx.Deadline(); !ok {
		return ErrInvalidPolicy
	}
	if len(r.outcomes) >= r.calls {
		return r.outcomes[r.calls-1]
	}
	return r.err
}

func TestSeatbeltProfileRulesAndValidation(t *testing.T) {
	t.Parallel()

	t.Run("symlinks and temp rules", func(t *testing.T) {
		t.Parallel()
		assertSeatbeltSymlinkAndTempRules(t)
	})
	t.Run("validation gate caching", func(t *testing.T) {
		t.Parallel()
		assertSeatbeltValidationGateCaching(t)
	})
	t.Run("validation gate timeout retry", func(t *testing.T) {
		t.Parallel()
		assertSeatbeltValidationTimeoutRetry(t)
	})
}

func assertSeatbeltSymlinkAndTempRules(t *testing.T) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(root, "real")
	if mkdirErr := os.MkdirAll(realDir, 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	linkDir := filepath.Join(root, "link")
	if symErr := os.Symlink(realDir, linkDir); symErr != nil {
		t.Fatal(symErr)
	}

	prof := buildSeatbeltProfile(&Policy{
		WorkDir:       linkDir,
		ReadOnlyPaths: []string{linkDir},
		DenyReadPaths: []string{linkDir},
		Env:           map[string]string{testEnvHome: root},
		Network:       NetworkNone,
	})
	for _, want := range []string{`(subpath "` + linkDir + `")`, `(subpath "` + realDir + `")`} {
		if !strings.Contains(prof, want) {
			t.Fatalf("missing %s in profile:\n%s", want, prof)
		}
	}

	tempProf := buildSeatbeltProfile(&Policy{
		WorkDir:        "/private/tmp",
		ReadOnlyPaths:  []string{"/private/tmp/dk-other"},
		ReadWritePaths: []string{"/private/tmp"},
		SessionTempDir: testSessionTempDir,
	})
	denyAt := strings.Index(tempProf, `(deny file-read* file-write* (regex #"^/private/tmp/dk-"))`)
	ownAt := strings.Index(tempProf, `(allow file-read* file-write* (subpath "/private/tmp/dk-12345678"))`)
	if denyAt <= strings.LastIndex(tempProf, `(subpath "/private/tmp/dk-other")`) || ownAt <= denyAt {
		t.Fatalf("session temp ordering broken:\n%s", tempProf)
	}
	if escapeSBPL(`a\b"c`) != `a\\b\"c` {
		t.Fatalf("escapeSBPL mismatch: %q", escapeSBPL(`a\b"c`))
	}
}

func assertSeatbeltValidationGateCaching(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	for _, failure := range []bool{false, true} {
		runner := &fakeProfileRunner{}
		if failure {
			runner.err = errProfileRejected()
		}
		gate := unixProfileGate{runner: runner}
		if !strings.Contains(gate.status(), "not been checked") {
			t.Fatal(gate.status())
		}
		p := Policy{WorkDir: testWorkDir, SessionTempDir: testSessionTempDir, DenyReadPaths: []string{"/secret"}}
		first := gate.profile(ctx, &p)
		p.SessionTempDir = "/private/tmp/dk-87654321"
		second := gate.profile(ctx, &p)
		if runner.calls != 1 || !strings.Contains(runner.profile, "local unix-socket") {
			t.Fatal("must probe full policy once")
		}
		for _, result := range []string{first, second} {
			if strings.Contains(result, "local unix-socket") == failure {
				t.Fatal("wrong optional rule fallback")
			}
		}
		if failure != strings.Contains(gate.status(), "unavailable") {
			t.Fatal(gate.status())
		}
	}
}

func assertSeatbeltValidationTimeoutRetry(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	for _, recoverOnRetry := range []bool{false, true} {
		outcomes := []error{context.DeadlineExceeded, context.DeadlineExceeded}
		if recoverOnRetry {
			outcomes[1] = nil
		}
		runner := &fakeProfileRunner{outcomes: outcomes}
		gate := unixProfileGate{runner: runner}
		p := Policy{WorkDir: testWorkDir, SessionTempDir: testSessionTempDir}
		noTemp := gate.profile(ctx, &Policy{WorkDir: testWorkDir})
		if strings.Contains(noTemp, "local unix-socket") {
			t.Fatal("empty SessionTempDir must not emit unix-socket rules")
		}
		result := gate.profile(ctx, &p)
		_ = gate.profile(ctx, &p)
		if runner.calls != 2 {
			t.Fatalf("timeout retry calls = %d, want 2", runner.calls)
		}
		if strings.Contains(result, "local unix-socket") != recoverOnRetry {
			t.Fatal("wrong outcome after timeout retry")
		}
	}
}

func TestBwrapArgsNestedBindsAndResolvConf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := filepath.Join(root, "billing")
	codexHome := filepath.Join(root, "codex-home")
	rules := filepath.Join(codexHome, "rules")
	stub := filepath.Join(root, "run", "systemd", "resolve", "stub-resolv.conf")
	for _, dir := range []string{service, rules, filepath.Dir(stub)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(stub, []byte("nameserver 127.0.0.53\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "resolv.conf")
	if err := os.Symlink(stub, link); err != nil {
		t.Fatal(err)
	}
	resolvedStub, err := filepath.EvalSymlinks(stub)
	if err != nil {
		t.Fatal(err)
	}

	env := defaultBwrapEnv()
	env.resolvConfPath = link
	args := buildBwrapArgsWith(&Policy{
		WorkDir:        service,
		ReadOnlyPaths:  []string{root},
		ReadWritePaths: []string{codexHome},
		DenyWritePaths: []string{rules},
		Env:            map[string]string{testEnvHome: filepath.Join(root, "home")},
		PrivateHome:    boolPtr(false),
		Network:        NetworkHost,
	}, Cmd{Path: testBinSh}, env)

	assertBwrapOrderAndFlags(t, args, root, service, codexHome, rules, resolvedStub)
}

func assertBwrapOrderAndFlags(t *testing.T, args []string, root, service, codexHome, rules, resolvedStub string) {
	t.Helper()
	for _, want := range []string{"--unshare-pid", "--die-with-parent"} {
		if !slices.Contains(args, want) {
			t.Fatalf("bwrap args missing %s", want)
		}
	}
	roParent := bindIndex(args, "--ro-bind", root)
	rwChild := bindIndex(args, "--bind", service)
	if roParent < 0 || rwChild < 0 || roParent > rwChild {
		t.Fatalf("expected parent RO bind (%d) before child RW bind (%d)", roParent, rwChild)
	}
	rwHome := bindIndex(args, "--bind", codexHome)
	roRules := bindIndex(args, "--ro-bind", rules)
	if rwHome < 0 || roRules < 0 || roRules < rwHome {
		t.Fatalf("expected RW codexHome (%d) before RO rules (%d)", rwHome, roRules)
	}
	if bindIndex(args, "--ro-bind", resolvedStub) < 0 {
		t.Fatalf("expected resolv.conf target %q to be bound in %v", resolvedStub, args)
	}
}

func bindIndex(args []string, flag, path string) int {
	for i := 0; i+2 < len(args); i++ {
		if args[i] == flag && args[i+1] == path && args[i+2] == path {
			return i
		}
	}
	return -1
}
