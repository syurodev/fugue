package kit_test

import (
	"testing"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/kit/icondata"
)

// TestFileIconLookupPrefersFilenameThenExtensionThenGeneric pins the
// Symbols lookup order: full filename → extension → generic file icon.
func TestFileIconLookupPrefersFilenameThenExtensionThenGeneric(t *testing.T) {
	t.Parallel()

	// Full filename match: go.mod → go-mod (pink), not the .mod extension.
	goMod := kit.FileIcon("go.mod")
	if goMod.Color != "pink" {
		t.Fatalf("go.mod = %q color, want pink", goMod.Color)
	}
	if goMod.Fill == "" {
		t.Fatalf("go.mod has no fill path, want go-mod icon")
	}

	// Extension match: main.go → go (blue via #60A5FA).
	mainGo := kit.FileIcon("main.go")
	if mainGo.Color != "blue" {
		t.Fatalf("main.go = %q color, want blue", mainGo.Color)
	}

	// Unknown extension → generic document (gray).
	fooXyz := kit.FileIcon("foo.xyz")
	generic := icondata.FileIcons[icondata.FileGeneric]
	if fooXyz.Fill != generic.Fill {
		t.Fatalf("foo.xyz = %+v, want generic document icon", fooXyz)
	}

	// Dockerfile full name → docker (blue via #60A5FA).
	dockerfile := kit.FileIcon("Dockerfile")
	if dockerfile.Color != "blue" {
		t.Fatalf("Dockerfile = %q color, want blue", dockerfile.Color)
	}

	// Case-insensitive: Go.Mod → go-mod.
	goModUpper := kit.FileIcon("Go.Mod")
	if goModUpper.Fill != goMod.Fill {
		t.Fatalf("Go.Mod does not match go.mod (case-insensitive)")
	}

	// Generic file icon is "document".
	if icondata.FileGeneric != "document" {
		t.Fatalf("FileGeneric = %q, want document", icondata.FileGeneric)
	}

	// Folder name: doc → folder-documents (gray).
	doc := kit.FolderIcon("doc")
	if doc.Color != "gray" {
		t.Fatalf("doc = %q color, want gray", doc.Color)
	}

	// Unknown folder → generic folder (gray).
	src := kit.FolderIcon("src")
	folderGen := icondata.FileIcons[icondata.FolderGeneric]
	if src.Fill != folderGen.Fill {
		t.Fatalf("src = %+v, want FolderGeneric (%q)", src, icondata.FolderGeneric)
	}

	// Generic folder icon is "folder", not "document" (avoids collision
	// with Lucide's Folder constant).
	if icondata.FolderGeneric == icondata.FileGeneric {
		t.Fatalf("FolderGeneric (%q) must not equal FileGeneric (%q)", icondata.FolderGeneric, icondata.FileGeneric)
	}
	if icondata.FolderGeneric != "folder" {
		t.Fatalf("FolderGeneric = %q, want folder", icondata.FolderGeneric)
	}
}
