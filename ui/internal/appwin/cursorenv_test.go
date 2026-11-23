package appwin

import "testing"

const (
	keySize   = "cursor-size"
	keyTheme  = "cursor-theme"
	themeYaru = "Yaru"
)

func TestMatchCursor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		env                map[string]string
		settings           map[string]string
		wantSize, wantName string
	}{
		{"GNOME session with nothing exported", nil, map[string]string{keySize: "24", keyTheme: themeYaru}, "24", themeYaru},
		{"user scaled the desktop cursor", nil, map[string]string{keySize: "48", keyTheme: "Adwaita"}, "48", "Adwaita"},
		{"no gsettings: the stock size, not Gio's 32", nil, nil, "24", ""},
		{"unparsable size", nil, map[string]string{keySize: "big"}, "24", ""},
		{"session values win", map[string]string{"XCURSOR_SIZE": "32", "XCURSOR_THEME": "DMZ"}, map[string]string{keySize: "24", keyTheme: themeYaru}, "32", "DMZ"},
	} {
		env := map[string]string{}
		for k, v := range tc.env {
			env[k] = v
		}
		err := matchCursor(func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(k, v string) error { env[k] = v; return nil },
			func(k string) string { return tc.settings[k] })
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if env["XCURSOR_SIZE"] != tc.wantSize || env["XCURSOR_THEME"] != tc.wantName {
			t.Errorf("%s: size %q theme %q, want %q %q", tc.name, env["XCURSOR_SIZE"], env["XCURSOR_THEME"], tc.wantSize, tc.wantName)
		}
	}
}

func TestParseGSetting(t *testing.T) {
	t.Parallel()
	out := "org.gnome.desktop.interface cursor-size 24\norg.gnome.desktop.interface cursor-theme 'Yaru'\n"
	if got := parseGSetting(out, keySize); got != "24" {
		t.Errorf("cursor-size = %q, want 24", got)
	}
	if got := parseGSetting(out, keyTheme); got != themeYaru {
		t.Errorf("cursor-theme = %q, want Yaru", got)
	}
	if got := parseGSetting(out, "missing"); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
}
