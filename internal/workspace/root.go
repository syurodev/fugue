package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RootAccess specifies access level for a workspace root.
type RootAccess string

// Access levels for a workspace root.
const (
	AccessReadWrite RootAccess = "rw"
	AccessReadOnly  RootAccess = "ro"
)

var rootNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Root represents a named root directory in a workspace.
type Root struct {
	Name   string     `json:"name"`
	Path   string     `json:"path"` // Canonical absolute path on disk
	Access RootAccess `json:"access"`
}

// RootSpec specifies input for creating or updating a root.
type RootSpec struct {
	Name   string
	Path   string
	Access RootAccess
}

// ValidateRootName verifies that name matches ^[a-z0-9][a-z0-9-]{0,31}$.
func ValidateRootName(name string) error {
	if !rootNameRegex.MatchString(name) {
		return NewError(ErrRootInvalid, fmt.Sprintf("invalid root name %q: must match ^[a-z0-9][a-z0-9-]{0,31}$", name))
	}
	return nil
}

// ValidateRootAccess verifies that access is either "rw" or "ro".
func ValidateRootAccess(access RootAccess) error {
	if access != AccessReadWrite && access != AccessReadOnly {
		return NewError(ErrRootInvalid, fmt.Sprintf("invalid root access %q: must be 'rw' or 'ro'", access))
	}
	return nil
}

// ValidateAndResolveRootPath expands ~, resolves symlinks, checks directory existence,
// and rejects '/' or any path inside Fugue home or the data directory.
func ValidateAndResolveRootPath(rawPath, homeDir, fugueHome, dataDir string) (string, error) {
	expanded := expandHome(rawPath, homeDir)
	if !filepath.IsAbs(expanded) {
		return "", NewError(ErrRootInvalid, "root path must be absolute: "+rawPath)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Clean(expanded))
	if err != nil {
		if os.IsNotExist(err) {
			return "", NewError(ErrRootInvalid, "root path does not exist: "+rawPath)
		}
		return "", WrapError(ErrRootInvalid, "resolve root path "+rawPath, err)
	}

	stat, statErr := os.Stat(resolved)
	if statErr != nil {
		return "", WrapError(ErrRootInvalid, "stat root path "+resolved, statErr)
	}
	if !stat.IsDir() {
		return "", NewError(ErrRootInvalid, "root path is not a directory: "+rawPath)
	}

	canonical := filepath.Clean(resolved)
	if canonical == "/" {
		return "", NewError(ErrPathDenied, "root cannot be the filesystem root '/'")
	}

	if checkErr := checkForbiddenZones(canonical, fugueHome, dataDir); checkErr != nil {
		return "", checkErr
	}
	return canonical, nil
}

func checkForbiddenZones(canonical, fugueHome, dataDir string) error {
	for _, z := range []struct{ name, path string }{{"Fugue home", fugueHome}, {"data directory", dataDir}} {
		if z.path != "" {
			if can := canonicalPath(z.path); canonical == can || isSubPath(canonical, can) {
				return NewError(ErrPathDenied, fmt.Sprintf("root path %s is inside %s %s", canonical, z.name, z.path))
			}
		}
	}
	return nil
}

func canonicalPath(p string) string {
	clean := filepath.Clean(p)
	if eval, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(eval)
	}
	return clean
}

func isSubPath(target, parent string) bool {
	rel, err := filepath.Rel(parent, target)
	return err == nil && !strings.HasPrefix(rel, "..") && rel != "."
}

func expandHome(p, homeDir string) string {
	if p == "~" {
		return homeDir
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir, p[2:])
	}
	return p
}

// CheckRootOverlap checks that newRoot does not share a name or canonical path with
// existing roots in the workspace, and that it is not nested inside any of them.
func CheckRootOverlap(existing []Root, newRoot Root) error {
	for _, ext := range existing {
		if ext.Name == newRoot.Name {
			return NewError(ErrRootInvalid, fmt.Sprintf("duplicate root name %q", newRoot.Name))
		}
		if ext.Path == newRoot.Path {
			return NewError(ErrRootInvalid, "duplicate root path "+newRoot.Path)
		}
		if isSubPath(newRoot.Path, ext.Path) || isSubPath(ext.Path, newRoot.Path) {
			return NewError(ErrRootInvalid, fmt.Sprintf("root %s overlaps with existing root %s", newRoot.Path, ext.Path))
		}
	}
	return nil
}

// IsRootMissing reports whether the root no longer exists on disk as a directory
// or now resolves to a different canonical path (e.g. symlink changed).
func IsRootMissing(r Root) bool {
	stat, err := os.Stat(r.Path)
	if err != nil || !stat.IsDir() {
		return true
	}
	resolved, err := filepath.EvalSymlinks(r.Path)
	return err != nil || filepath.Clean(resolved) != filepath.Clean(r.Path)
}

// DetectMissingRoots returns the names of all roots that are currently missing on disk.
func DetectMissingRoots(roots []Root) []string {
	var missing []string
	for _, r := range roots {
		if IsRootMissing(r) {
			missing = append(missing, r.Name)
		}
	}
	return missing
}
