package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateWorkspaceMD builds the read-only WORKSPACE.md document for users and models.
func GenerateWorkspaceMD(id, name string, roots []Root) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Workspace: %s\n\n- **ID:** `%s`\n- **Name:** `%s`\n\n## Roots\n\n", name, id, name)
	if len(roots) == 0 {
		sb.WriteString("No roots configured.\n")
		return sb.String()
	}
	sb.WriteString("| Name | Path | Access |\n| --- | --- | --- |\n")
	for _, r := range roots {
		fmt.Fprintf(&sb, "| %s | `%s` | %s |\n", r.Name, r.Path, r.Access)
	}
	return sb.String()
}

func writeAtomic(dir, filename string, content []byte) error {
	tmpFile, err := os.CreateTemp(dir, "."+filename+".tmp-*")
	if err != nil {
		return WrapError(ErrOwnershipInvalid, "create temp file", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if remErr := os.Remove(tmpName); remErr != nil {
			return
		}
	}()
	defer func() {
		if cErr := tmpFile.Close(); cErr != nil {
			return
		}
	}()

	if err := tmpFile.Chmod(filePerm); err != nil {
		return WrapError(ErrOwnershipInvalid, "chmod temp file", err)
	}
	if _, err := tmpFile.Write(content); err != nil {
		return WrapError(ErrOwnershipInvalid, "write temp file", err)
	}
	if err := tmpFile.Close(); err != nil {
		return WrapError(ErrOwnershipInvalid, "close temp file", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, filename)); err != nil {
		return WrapError(ErrOwnershipInvalid, "rename temp file", err)
	}
	return nil
}
