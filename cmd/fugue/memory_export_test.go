package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	testMemExportWSName = "my-ws"
	testMemExportPath   = "/tmp/mem.md"
	testMemExportBadArg = "--bad-flag"
)

func appAddForCLITest(wsID string) app.MemoryWriteCommand {
	return app.MemoryWriteCommand{
		WorkspaceID: wsID,
		Op:          app.MemoryOpAdd,
		Kind:        app.MemoryKindFact,
		Label:       "cli-export-label",
		Summary:     "CLI export summary.",
		Body:        "This is a memory entry that verifies the CLI export output and file mode.",
	}
}

func TestParseMemoryExportArgs(t *testing.T) {
	t.Parallel()
	valid := []struct {
		args     []string
		wantWS   string
		wantPath string
		wantSock string
	}{
		{[]string{memoryCommand, memoryExportSubcommand, "ws-1", "out.md"}, "ws-1", "out.md", ""},
		{[]string{memoryCommand, memoryExportSubcommand, testMemExportWSName, testMemExportPath, exclusiveSocketFlag, testPruneSocket}, testMemExportWSName, testMemExportPath, testPruneSocket},
		{[]string{memoryCommand, memoryExportSubcommand, exclusiveSocketFlag + "=" + testPruneSocket, testMemExportWSName, testMemExportPath}, testMemExportWSName, testMemExportPath, testPruneSocket},
	}
	for _, tc := range valid {
		ws, p, sock, matched, ok := parseMemoryExportArgs(tc.args)
		if !matched || !ok || ws != tc.wantWS || p != tc.wantPath || sock != tc.wantSock {
			t.Fatalf("parseMemoryExportArgs(%v) = (%q, %q, %q, %v, %v)", tc.args, ws, p, sock, matched, ok)
		}
	}
	for _, bad := range [][]string{
		{memoryCommand, memoryExportSubcommand},
		{memoryCommand, memoryExportSubcommand, "ws-only"},
		{memoryCommand, memoryExportSubcommand, "ws", "p", "extra"},
		{memoryCommand, memoryExportSubcommand, "ws", "p", exclusiveSocketFlag},
		{memoryCommand, memoryExportSubcommand, "ws", "p", testMemExportBadArg},
	} {
		_, _, _, matched, ok := parseMemoryExportArgs(bad)
		if !matched || ok {
			t.Fatalf("parseMemoryExportArgs(%v) = (%v, %v), want matched=true valid=false", bad, matched, ok)
		}
	}
	var out bytes.Buffer
	if code := runMain(t.Context(), []string{memoryCommand, memoryExportSubcommand, "only-one"}, &out, &bytes.Buffer{}); code != exitUsage || !strings.Contains(out.String(), "fugue memory export") {
		t.Fatalf("runMain invalid memory export = (%d, %q), want exitUsage with usage", code, out.String())
	}
}

func TestMemoryExportCLI(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sockPath, wsID := setupMemoryExportCLITest(t, "export-cli-ws")

	outDir := t.TempDir()
	byNamePath := filepath.Join(outDir, "by-name.md")
	var out, errOut bytes.Buffer
	if code := RunMemoryExport(ctx, &out, &errOut, config.MapEnv{}, "export-cli-ws", byNamePath, sockPath, testVersion); code != 0 {
		t.Fatalf("RunMemoryExport by name exit=%d stderr=%q", code, errOut.String())
	}
	assertExportedFile0600(t, outDir, "by-name.md")

	errOut.Reset()
	if code := RunMemoryExport(ctx, &out, &errOut, config.MapEnv{}, wsID, byNamePath, sockPath, testVersion); code == 0 {
		t.Fatal("RunMemoryExport overwrote existing file")
	}

	byIDPath := filepath.Join(outDir, "by-id.md")
	errOut.Reset()
	if code := RunMemoryExport(ctx, &out, &errOut, config.MapEnv{}, wsID, byIDPath, sockPath, testVersion); code != 0 {
		t.Fatalf("RunMemoryExport by id exit=%d stderr=%q", code, errOut.String())
	}

	errOut.Reset()
	missingPath := filepath.Join(outDir, "missing.md")
	if code := RunMemoryExport(ctx, &out, &errOut, config.MapEnv{}, "no-such-ws", missingPath, sockPath, testVersion); code == 0 || !strings.Contains(errOut.String(), "workspace.not_found") {
		t.Fatalf("RunMemoryExport unknown workspace = (code=%d, stderr=%q), want non-zero with workspace.not_found", code, errOut.String())
	}
}

func setupMemoryExportCLITest(t *testing.T, wsName string) (sockPath, wsID string) {
	t.Helper()
	ctx := t.Context()
	sockPath, hp := startExclusiveTestHost(t, 2)
	cli, err := client.Dial(ctx, sockPath, client.Params{
		Kind: client.KindCLI, Name: "test-cli", Version: testVersion, APIVersions: []string{"v1"},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cli.Close()

	if createErr := cli.CreateWorkspace(ctx, wsName); createErr != nil {
		t.Fatalf("CreateWorkspace: %v", createErr)
	}
	wsList, err := cli.ListWorkspaces(ctx)
	if err != nil || len(wsList) != 1 {
		t.Fatalf("ListWorkspaces = (%+v, %v)", wsList, err)
	}
	wsID = wsList[0].ID

	memSvc, err := hp.Runtime().MemoryService()
	if err != nil {
		t.Fatalf("MemoryService: %v", err)
	}
	cmd := appAddForCLITest(wsID)
	if _, addErr := memSvc.WriteEntry(ctx, string(client.KindCLI), &cmd); addErr != nil {
		t.Fatalf("memSvc.WriteEntry: %v", addErr)
	}
	return sockPath, wsID
}

func assertExportedFile0600(t *testing.T, dir, name string) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer closeRootQuietly(root)
	info, err := root.Stat(name)
	if err != nil {
		t.Fatalf("Stat exported file: %v", err)
	}
	if info.Mode().Perm() != memoryExportFileMode {
		t.Fatalf("exported file perm = %o, want 0600", info.Mode().Perm())
	}
	data, err := root.ReadFile(name)
	if err != nil || !strings.Contains(string(data), "format: fugue-memory/1") || !strings.Contains(string(data), "cli-export-label") {
		t.Fatalf("exported file content unexpected: err=%v len=%d", err, len(data))
	}
}
