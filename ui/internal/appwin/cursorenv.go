package appwin

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Cursor size (FUG-58). On Wayland, Gio v0.10.3 loads its cursor theme at
// `XCURSOR_SIZE` times the output scale and falls back to 32 when the
// variable is unset or not a number (gioui.org@v0.10.3/app/os_wayland.go
// lines 386-396). GNOME does not export XCURSOR_SIZE to a session's clients
// (GTK and Mutter read org.gnome.desktop.interface directly) and its default
// size is 24, so a Fugue window showed a 32 px cursor against 24 px on the
// desktop. Gio offers no option for the size, so the app sets the variable
// itself before its first window exists, from the same source the desktop
// uses, and never overrides a value the user or session already set.

// defaultCursorSize is the size of the stock desktop cursor theme.
const defaultCursorSize = 24

// MatchSystemCursor makes the window's cursor the size and theme of the
// desktop's: it fills XCURSOR_SIZE and XCURSOR_THEME when the session left
// them unset. Call it before the first window opens, from the main
// goroutine. Platforms that do not draw through Wayland ignore the
// variables, so it is harmless there.
func MatchSystemCursor(ctx context.Context, lookup func(string) (string, bool), setenv func(key, value string) error) error {
	return matchCursor(lookup, setenv, func(key string) string { return gnomeSetting(ctx, key) })
}

func matchCursor(lookup func(string) (string, bool), setenv func(key, value string) error, gsetting func(key string) string) error {
	if v, _ := lookup("XCURSOR_SIZE"); v == "" {
		size := defaultCursorSize
		if n, err := strconv.Atoi(gsetting("cursor-size")); err == nil && n > 0 {
			size = n
		}
		if err := setenv("XCURSOR_SIZE", strconv.Itoa(size)); err != nil {
			return fmt.Errorf("set XCURSOR_SIZE: %w", err)
		}
	}
	if v, _ := lookup("XCURSOR_THEME"); v == "" {
		if name := gsetting("cursor-theme"); name != "" {
			if err := setenv("XCURSOR_THEME", name); err != nil {
				return fmt.Errorf("set XCURSOR_THEME: %w", err)
			}
		}
	}
	return nil
}

const gsettingsTimeout = time.Second

// gnomeSetting reads one org.gnome.desktop.interface key, or "" when
// gsettings is missing, fails or times out (a non-GNOME session). The command
// is fixed and its output parsed, so no caller value reaches the process.
func gnomeSetting(ctx context.Context, key string) string {
	ctx, cancel := context.WithTimeout(ctx, gsettingsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "list-recursively", "org.gnome.desktop.interface").Output()
	if err != nil {
		return ""
	}
	return parseGSetting(string(out), key)
}

// parseGSetting finds `<schema> <key> <value>` in list-recursively output
// and strips the quotes of a string value.
func parseGSetting(out, key string) string {
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == key {
			return strings.Trim(strings.Join(fields[2:], " "), "'\"")
		}
	}
	return ""
}
