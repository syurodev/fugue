package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	memoryCommand          = "memory"
	memoryExportSubcommand = "export"
	memoryExportClientName = "fugue"
	memoryExportFileMode   = 0o600
)

var errMemoryWorkspaceNotFound = errors.New("workspace not found")

// parseMemoryExportArgs parses `memory export <workspace> <path> [--socket <path>]`.
func parseMemoryExportArgs(args []string) (workspace, outPath, socket string, matched, valid bool) {
	if len(args) < 2 || args[0] != memoryCommand || args[1] != memoryExportSubcommand {
		return "", "", "", false, false
	}
	rest := args[2:]
	positional := make([]string, 0, 2)
	for len(rest) > 0 {
		if val, next, isSock, ok := consumeFlagValue(rest, exclusiveSocketFlag); isSock {
			if !ok {
				return "", "", "", true, false
			}
			socket, rest = val, next
			continue
		}
		arg := rest[0]
		if len(arg) > 0 && arg[0] == '-' {
			return "", "", "", true, false
		}
		positional = append(positional, arg)
		rest = rest[1:]
	}
	if len(positional) != 2 || positional[0] == "" || positional[1] == "" {
		return "", "", "", true, false
	}
	return positional[0], positional[1], socket, true, true
}

// RunMemoryExport exports workspace memory to a local Markdown file via the CLI client kind.
func RunMemoryExport(ctx context.Context, out, errOut io.Writer, env config.Env, workspaceArg, outPath, socket, ver string) int {
	resolvedSocket, err := resolveExclusiveSocket(env, socket)
	if err != nil {
		writeMemoryCommandErr(errOut, err)
		return exitFailure
	}
	root, baseName, err := openExportTargetDir(outPath)
	if err != nil {
		writeMemoryCommandErr(errOut, err)
		return exitFailure
	}
	defer closeRootQuietly(root)
	if _, statErr := root.Lstat(baseName); statErr == nil {
		writeMemoryCommandErr(errOut, fmt.Errorf("output file %q already exists: %w", outPath, os.ErrExist))
		return exitFailure
	}
	cli, dialErr := client.Dial(ctx, resolvedSocket, client.Params{
		Kind:        client.KindCLI,
		Name:        memoryExportClientName,
		Version:     ver,
		APIVersions: []string{"v1"},
	})
	if dialErr != nil {
		writeMemoryCommandErr(errOut, dialErr)
		return exitFailure
	}
	defer cli.Close()

	wsID, err := resolveMemoryWorkspaceID(ctx, cli, workspaceArg)
	if err != nil {
		writeMemoryCommandErr(errOut, err)
		return exitFailure
	}
	content, err := cli.ExportMemory(ctx, wsID)
	if err != nil {
		writeMemoryCommandErr(errOut, err)
		return exitFailure
	}
	if err := writeExclusiveFile0600(root, baseName, content); err != nil {
		writeMemoryCommandErr(errOut, err)
		return exitFailure
	}
	if err := say(out, "exported %d bytes to %s", len(content), outPath); err != nil {
		writeMemoryCommandErr(errOut, err)
		return exitFailure
	}
	return 0
}

func openExportTargetDir(outPath string) (*os.Root, string, error) {
	absPath, err := filepath.Abs(outPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve export path: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(absPath))
	if err != nil {
		return nil, "", fmt.Errorf("open export directory: %w", err)
	}
	return root, filepath.Base(absPath), nil
}

func closeRootQuietly(root *os.Root) {
	if root == nil {
		return
	}
	if err := root.Close(); err != nil {
		slog.Warn("close export root", "err", err)
	}
}

func resolveMemoryWorkspaceID(ctx context.Context, cli *client.Client, workspaceArg string) (string, error) {
	list, err := cli.ListWorkspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}
	for _, ws := range list {
		if ws.ID == workspaceArg || ws.Name == workspaceArg {
			return ws.ID, nil
		}
	}
	return "", fmt.Errorf("workspace.not_found: %w (%s)", errMemoryWorkspaceNotFound, workspaceArg)
}

func writeExclusiveFile0600(root *os.Root, baseName string, data []byte) (retErr error) {
	f, err := root.OpenFile(baseName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, memoryExportFileMode)
	if err != nil {
		return fmt.Errorf("create export file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("close export file: %w", cerr)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write export file: %w", err)
	}
	return nil
}

func writeMemoryCommandErr(errOut io.Writer, err error) {
	if err == nil || errOut == nil {
		return
	}
	if code, ok := client.WorkspaceCodeOf(err); ok && code != "" {
		if serr := say(errOut, "fugue: %s: %v", code, err); serr != nil {
			slog.Error("write memory command error", "err", serr)
		}
		return
	}
	writeCommandErr(errOut, err)
}
