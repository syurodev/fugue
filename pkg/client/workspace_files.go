package client

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
)

// The read-only workspace file reads of host-api v0.18 §5.4 (F10.4e). The
// app's viewer calls these; the host confines every path to the session's
// workspace roots and answers with typed public codes, which the typed
// errors below surface for errors.Is.

// Public workspace file-read error codes returned by the Host API.
const (
	WorkspaceCodePathOutsideRoot = "workspace.path_outside_root"
	WorkspaceCodeFileNotFound    = "workspace.file_not_found"
	WorkspaceCodeFileTooLarge    = "workspace.file_too_large"
	WorkspaceCodeNotText         = "workspace.not_text"
)

// Typed workspace file-read sentinels for errors.Is.
var (
	ErrWorkspacePathOutsideRoot = errors.New("the path resolves outside the workspace root")
	ErrWorkspaceFileNotFound    = errors.New("the file does not exist in the workspace root")
	ErrWorkspaceFileTooLarge    = errors.New("the file exceeds the 1 MiB read limit")
	ErrWorkspaceNotText         = errors.New("the file is not UTF-8 text")
)

// WorkspaceFile is one ReadWorkspaceFile answer.
type WorkspaceFile struct {
	Content    string
	TotalLines int64
	Language   string
	Truncated  bool
}

// WorkspaceDirEntry is one ListWorkspaceDir answer entry.
type WorkspaceDirEntry struct {
	Name string
	Kind string // "file" or "dir"
	Size int64
}

// WorkspaceFileReadError reports a refused file or directory read, wrapping
// the underlying connect error so WorkspaceCodeOf still reads the public
// code and errors.Is matches the typed sentinel.
type WorkspaceFileReadError struct {
	Cause error
}

// Error implements error.
func (e *WorkspaceFileReadError) Error() string { return e.Cause.Error() }

// Unwrap returns the underlying connect error.
func (e *WorkspaceFileReadError) Unwrap() error { return e.Cause }

// Is matches the sentinel the host's public code names.
func (e *WorkspaceFileReadError) Is(target error) bool {
	switch target {
	case ErrWorkspacePathOutsideRoot:
		code, _ := WorkspaceCodeOf(e.Cause)
		return code == WorkspaceCodePathOutsideRoot
	case ErrWorkspaceFileNotFound:
		code, _ := WorkspaceCodeOf(e.Cause)
		return code == WorkspaceCodeFileNotFound
	case ErrWorkspaceFileTooLarge:
		code, _ := WorkspaceCodeOf(e.Cause)
		return code == WorkspaceCodeFileTooLarge
	case ErrWorkspaceNotText:
		code, _ := WorkspaceCodeOf(e.Cause)
		return code == WorkspaceCodeNotText
	default:
		return false
	}
}

// ReadWorkspaceFile reads a UTF-8 text file inside one of workspaceID's
// registered roots. path is root-relative; startLine and endLine are a
// 1-based inclusive range with zero meaning unset. A refused read — outside
// the root, missing, over the 1 MiB limit, or not text — comes back wrapped
// in a WorkspaceFileReadError matching the typed sentinel.
func (c *Client) ReadWorkspaceFile(ctx context.Context, workspaceID, rootName, path string, startLine, endLine int64) (WorkspaceFile, error) {
	res, err := workspaceClient(c).ReadWorkspaceFile(ctx,
		connect.NewRequest(&workspacev1.ReadWorkspaceFileRequest{
			WorkspaceId: workspaceID, RootName: rootName, Path: path,
			StartLine: startLine, EndLine: endLine,
		}))
	if err != nil {
		return WorkspaceFile{}, fmt.Errorf("read the workspace file: %w", &WorkspaceFileReadError{Cause: err})
	}
	return WorkspaceFile{
		Content:    res.Msg.GetContent(),
		TotalLines: res.Msg.GetTotalLines(),
		Language:   res.Msg.GetLanguage(),
		Truncated:  res.Msg.GetTruncated(),
	}, nil
}

// ListWorkspaceDir lists one directory inside workspaceID's registered root,
// sorted by name and capped at 1,000 entries. An empty path lists the root.
func (c *Client) ListWorkspaceDir(ctx context.Context, workspaceID, rootName, path string) ([]WorkspaceDirEntry, error) {
	res, err := workspaceClient(c).ListWorkspaceDir(ctx,
		connect.NewRequest(&workspacev1.ListWorkspaceDirRequest{
			WorkspaceId: workspaceID, RootName: rootName, Path: path,
		}))
	if err != nil {
		return nil, fmt.Errorf("list the workspace directory: %w", &WorkspaceFileReadError{Cause: err})
	}
	entries := res.Msg.GetEntries()
	out := make([]WorkspaceDirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, WorkspaceDirEntry{Name: e.GetName(), Kind: e.GetKind(), Size: e.GetSize()})
	}
	return out, nil
}
