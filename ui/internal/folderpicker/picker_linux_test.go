//go:build linux

package folderpicker

import "testing"

func TestPortalCallSpecModes(t *testing.T) {
	t.Parallel()

	method, opts := portalCallSpec(1, Request{Mode: ModeFolder})
	if method != portalOpenMethod {
		t.Fatalf("folder method = %q, want %q", method, portalOpenMethod)
	}
	if dir, ok := opts["directory"].Value().(bool); !ok || !dir {
		t.Fatalf("folder directory opt = %+v, want true", opts["directory"])
	}

	method, opts = portalCallSpec(2, Request{Mode: ModeOpenFile, FilterExt: ".md"})
	if method != portalOpenMethod {
		t.Fatalf("open file method = %q, want %q", method, portalOpenMethod)
	}
	if _, ok := opts["filters"]; !ok {
		t.Fatal("open file missing filters opt")
	}

	method, opts = portalCallSpec(3, Request{Mode: ModeSaveFile, DefaultName: "ws-memory.md"})
	if method != portalSaveMethod {
		t.Fatalf("save file method = %q, want %q", method, portalSaveMethod)
	}
	if cur, ok := opts["current_name"].Value().(string); !ok || cur != "ws-memory.md" {
		t.Fatalf("save file current_name = %+v, want ws-memory.md", opts["current_name"])
	}
}
