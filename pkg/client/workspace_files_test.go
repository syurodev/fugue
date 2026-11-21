package client_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

// The RPC names the fake scripts failures for.
const (
	rpcReadWorkspaceFile = "ReadWorkspaceFile"
	rpcListWorkspaceDir  = "ListWorkspaceDir"
)

// The request arguments every scripted call in the F10.4e client test makes,
// so the fake can assert the request round-trips.
const (
	viewerWS   = "ws-1"
	viewerRoot = "main"
	viewerPath = "src/notes.md"
	viewerDir  = "src"
)

// workspaceFileFake is a scripted workspace host: the two F10.4e read RPCs
// answer fixed bodies or coded refusals, so the client's typed errors are
// exercised against the real wire shapes.
type workspaceFileFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	workspacev1connect.UnimplementedWorkspaceServiceHandler

	mu        sync.Mutex
	content   string
	lines     int64
	language  string
	truncated bool
	entries   []*workspacev1.DirEntry
	fail      map[string]string // rpc name -> public error code
}

func (f *workspaceFileFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (f *workspaceFileFake) coded(method string) error {
	f.mu.Lock()
	code, ok := f.fail[method]
	f.mu.Unlock()
	if !ok {
		return nil
	}
	status := connect.CodePermissionDenied
	switch code {
	case client.WorkspaceCodeFileNotFound:
		status = connect.CodeNotFound
	case client.WorkspaceCodeFileTooLarge:
		status = connect.CodeResourceExhausted
	case client.WorkspaceCodeNotText:
		status = connect.CodeInvalidArgument
	}
	cerr := connect.NewError(status, errClientInterScripted)
	detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code})
	if derr != nil {
		return cerr
	}
	cerr.AddDetail(detail)
	return cerr
}

func (f *workspaceFileFake) ReadWorkspaceFile(_ context.Context, req *connect.Request[workspacev1.ReadWorkspaceFileRequest]) (*connect.Response[workspacev1.ReadWorkspaceFileResponse], error) {
	if err := f.coded(rpcReadWorkspaceFile); err != nil {
		return nil, err
	}
	if req.Msg.Path != viewerPath || req.Msg.RootName != viewerRoot || req.Msg.WorkspaceId != viewerWS || req.Msg.StartLine != 10 || req.Msg.EndLine != 19 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errClientInterScripted)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&workspacev1.ReadWorkspaceFileResponse{
		Content: f.content, TotalLines: f.lines, Language: f.language, Truncated: f.truncated,
	}), nil
}

func (f *workspaceFileFake) ListWorkspaceDir(_ context.Context, req *connect.Request[workspacev1.ListWorkspaceDirRequest]) (*connect.Response[workspacev1.ListWorkspaceDirResponse], error) {
	if err := f.coded(rpcListWorkspaceDir); err != nil {
		return nil, err
	}
	if req.Msg.Path != viewerDir || req.Msg.RootName != viewerRoot || req.Msg.WorkspaceId != viewerWS {
		return nil, connect.NewError(connect.CodeInvalidArgument, errClientInterScripted)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&workspacev1.ListWorkspaceDirResponse{Entries: f.entries}), nil
}

func serveWorkspaceFileFake(t *testing.T, fake *workspaceFileFake) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(workspacev1connect.NewWorkspaceServiceHandler(fake))
	return serveFakeOnUnixSocket(t, mux)
}

// TestWorkspaceFileClientMethodsOverRealSocket drives the F10.4e read RPCs
// over a real unix socket: the happy path round-trips every field, and each
// refused read maps the host's public code onto its typed sentinel while the
// connect error stays readable through WorkspaceCodeOf.
func TestWorkspaceFileClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fake := &workspaceFileFake{
		content: "line 10\nline 11\n", lines: 300, language: "markdown", truncated: true,
		entries: []*workspacev1.DirEntry{
			{Name: "notes.md", Kind: "file", Size: 24},
			{Name: "nested", Kind: "dir"},
		},
	}
	c := serveWorkspaceFileFake(t, fake)

	file, err := c.ReadWorkspaceFile(ctx, viewerWS, viewerRoot, viewerPath, 10, 19)
	if err != nil {
		t.Fatalf("ReadWorkspaceFile: %v", err)
	}
	if file.Content != "line 10\nline 11\n" || file.TotalLines != 300 || file.Language != "markdown" || !file.Truncated {
		t.Fatalf("ReadWorkspaceFile = %+v", file)
	}

	assertListedDirEntries(t, ctx, c)

	assertFileReadRefusalsAreTyped(t, ctx, c, fake)
}

// assertListedDirEntries pins the listing round-trip: kinds, sizes, order.
func assertListedDirEntries(t *testing.T, ctx context.Context, c *client.Client) {
	t.Helper()
	entries, err := c.ListWorkspaceDir(ctx, viewerWS, viewerRoot, viewerDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("ListWorkspaceDir = (%+v, %v)", entries, err)
	}
	if entries[0].Name != "notes.md" || entries[0].Kind != "file" || entries[0].Size != 24 {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[1].Name != "nested" || entries[1].Kind != "dir" {
		t.Fatalf("second entry = %+v", entries[1])
	}
}

// assertFileReadRefusalsAreTyped scripts one refusal at a time and checks
// both the typed sentinel and the public code the client keeps readable.
func assertFileReadRefusalsAreTyped(t *testing.T, ctx context.Context, c *client.Client, fake *workspaceFileFake) {
	t.Helper()
	for name, tc := range map[string]struct {
		rpc      string
		code     string
		sentinel error
	}{
		"outside":  {rpcReadWorkspaceFile, client.WorkspaceCodePathOutsideRoot, client.ErrWorkspacePathOutsideRoot},
		"notfound": {rpcReadWorkspaceFile, client.WorkspaceCodeFileNotFound, client.ErrWorkspaceFileNotFound},
		"toolarge": {rpcReadWorkspaceFile, client.WorkspaceCodeFileTooLarge, client.ErrWorkspaceFileTooLarge},
		"nottxt":   {rpcReadWorkspaceFile, client.WorkspaceCodeNotText, client.ErrWorkspaceNotText},
		"listdeny": {rpcListWorkspaceDir, client.WorkspaceCodePathOutsideRoot, client.ErrWorkspacePathOutsideRoot},
	} {
		fake.mu.Lock()
		fake.fail = map[string]string{tc.rpc: tc.code}
		fake.mu.Unlock()
		var err error
		if tc.rpc == rpcReadWorkspaceFile {
			_, err = c.ReadWorkspaceFile(ctx, viewerWS, viewerRoot, viewerPath, 10, 19)
		} else {
			_, err = c.ListWorkspaceDir(ctx, viewerWS, viewerRoot, viewerDir)
		}
		if !errors.Is(err, tc.sentinel) {
			t.Fatalf("%s: error = %v, want it to match %v", name, err, tc.sentinel)
		}
		if got, ok := client.WorkspaceCodeOf(err); !ok || got != tc.code {
			t.Fatalf("%s: WorkspaceCodeOf = (%q, %v), want (%q, true)", name, got, ok, tc.code)
		}
	}
}
