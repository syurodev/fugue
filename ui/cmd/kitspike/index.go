package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writeIndex writes shots/index.md: one line per PNG saying what it shows.
func writeIndex(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".png") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Kit spike screenshots\n\n")
	b.WriteString("Written by `go run ./cmd/kitspike -shots <dir>`. Tags: `px1`/`px125` are the\n")
	b.WriteString("platform pixel scale (1 / 1.25), `ui90`/`ui125` are the §3.0 UI scale on top of\n")
	b.WriteString("px1, `dark`/`light` is the mode. `-open`/`-submenu`/`-closed-again` are the\n")
	b.WriteString("menu's animation states.\n\n")
	b.WriteString("| File | What it shows |\n| --- | --- |\n")
	for _, n := range names {
		b.WriteString(fmt.Sprintf("| [%s](%s) | %s |\n", n, n, describeShot(n)))
	}
	return os.WriteFile(filepath.Join(dir, "index.md"), []byte(b.String()), 0o644)
}

// describeShot turns a file name into its caption.
func describeShot(name string) string {
	base := strings.TrimSuffix(name, ".png")
	parts := strings.SplitN(base, "-", 2)
	page := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	state := ""
	for _, st := range []string{"-closed-again", "-submenu", "-open", "-closed"} {
		if i := strings.Index(rest, st); i >= 0 {
			state = map[string]string{
				"-closed-again": "menu closed again after Escape (exit animation done)",
				"-submenu":      "menu with the Export submenu open via Right arrow",
				"-open":         "menu fully open",
				"-closed":       "menu closed before opening",
			}[st]
			rest = rest[:i] + rest[i+len(st):]
			break
		}
	}
	mode := "dark"
	scale := ""
	if strings.Contains(rest, "light") {
		mode = "light"
		rest = strings.Replace(rest, "light", "", 1)
	} else {
		rest = strings.Replace(rest, "dark", "", 1)
	}
	rest = strings.Trim(rest, "-")
	if rest == "px1" {
		scale = "pixel scale 1"
	} else if rest == "px125" {
		scale = "pixel scale 1.25"
	} else if rest == "ui90" {
		scale = "UI scale 90%"
	} else if rest == "ui125" {
		scale = "UI scale 125%"
	}
	desc := page + " page, " + mode + " mode, " + scale
	if state != "" {
		desc += "; " + state
	}
	return desc
}
