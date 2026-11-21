package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/syurodev/fugue/pkg/client"
)

// PrefsSchemaVersion is the JSON document version for fugue.appstate.v1
// preferences understood by this build.
const PrefsSchemaVersion = 1

// ThemeMode selects light, dark, or system theme preference.
type ThemeMode string

// Supported theme modes stored in app preferences.
const (
	ThemeSystem ThemeMode = "system"
	ThemeLight  ThemeMode = "light"
	ThemeDark   ThemeMode = "dark"
)

// Supported UI scale steps matching kit.Env.UIScale (ui-kit §3.0).
const (
	Scale090 float32 = 0.9
	Scale100 float32 = 1.0
	Scale110 float32 = 1.1
	Scale125 float32 = 1.25
)

// Preferences are the app-wide display preferences persisted via
// fugue.appstate.v1 outside the vault.
type Preferences struct {
	ThemeMode ThemeMode
	UIScale   float32
}

// DefaultPreferences returns the default app preferences (system theme, 100% scale).
func DefaultPreferences() Preferences {
	return Preferences{ThemeMode: ThemeSystem, UIScale: Scale100}
}

type preferencesWire struct {
	SchemaVersion int       `json:"schema_version"`
	ThemeMode     ThemeMode `json:"theme_mode,omitempty"`
	UIScale       float32   `json:"ui_scale,omitempty"`
}

// NormalizeThemeMode maps any unrecognized theme mode to ThemeSystem.
func NormalizeThemeMode(mode ThemeMode) ThemeMode {
	switch mode {
	case ThemeLight, ThemeDark, ThemeSystem:
		return mode
	default:
		return ThemeSystem
	}
}

// NormalizeUIScale snaps scale to the nearest supported step (90/100/110/125%).
func NormalizeUIScale(scale float32) float32 {
	if scale <= 0 || math.IsNaN(float64(scale)) || math.IsInf(float64(scale), 0) {
		return Scale100
	}
	steps := []float32{Scale090, Scale100, Scale110, Scale125}
	best := Scale100
	bestDiff := float32(math.Abs(float64(scale - Scale100)))
	for _, s := range steps {
		if d := float32(math.Abs(float64(scale - s))); d < bestDiff {
			best = s
			bestDiff = d
		}
	}
	return best
}

// UIScaleLabel formats a UI scale step as a short percentage label.
func UIScaleLabel(scale float32) string {
	switch NormalizeUIScale(scale) {
	case Scale090:
		return "90%"
	case Scale110:
		return "110%"
	case Scale125:
		return "125%"
	default:
		return "100%"
	}
}

// DecodePreferences decodes a fugue.appstate.v1 preferences document. If
// schema_version exceeds PrefsSchemaVersion, higherSchema is true and defaults
// are returned so callers avoid overwriting a newer client's document.
func DecodePreferences(raw []byte) (prefs Preferences, higherSchema bool, err error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return DefaultPreferences(), false, nil
	}
	var wire preferencesWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return DefaultPreferences(), false, fmt.Errorf("decode app preferences: %w", err)
	}
	if wire.SchemaVersion > PrefsSchemaVersion {
		return DefaultPreferences(), true, nil
	}
	return Preferences{
		ThemeMode: NormalizeThemeMode(wire.ThemeMode),
		UIScale:   NormalizeUIScale(wire.UIScale),
	}, false, nil
}

// EncodePreferences marshals p into the versioned JSON envelope expected by
// fugue.appstate.v1.
func EncodePreferences(p Preferences) ([]byte, error) {
	wire := preferencesWire{
		SchemaVersion: PrefsSchemaVersion,
		ThemeMode:     NormalizeThemeMode(p.ThemeMode),
		UIScale:       NormalizeUIScale(p.UIScale),
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode app preferences: %w", err)
	}
	return raw, nil
}

// PrefsState tracks the app preferences document, its CAS revision, and any
// in-flight save state.
type PrefsState struct {
	Loaded       bool
	Revision     uint64
	Current      Preferences
	HigherSchema bool
	Saving       bool
	Err          string
	cmdSeq       uint64
}

// NextCommandID produces a deterministic per-window command ID for PutPreferences.
func (p *PrefsState) NextCommandID() string {
	p.cmdSeq++
	return fmt.Sprintf("settings-prefs-%d", p.cmdSeq)
}

// ApplyDocument decodes and stores preferences loaded at revision.
func (p *PrefsState) ApplyDocument(revision uint64, raw []byte) error {
	decoded, higher, err := DecodePreferences(raw)
	if err != nil {
		p.Err = err.Error()
		return err
	}
	p.Revision = revision
	p.Loaded = true
	p.HigherSchema = higher
	p.Current = decoded
	p.Err = ""
	return nil
}

// ApplySnapshot folds an appstate topic snapshot into preferences state.
func (p *PrefsState) ApplySnapshot(snap *client.AppStateSnapshot) error {
	if snap == nil || snap.Preferences == nil {
		p.Loaded = true
		return nil
	}
	return p.ApplyDocument(snap.Preferences.Revision, snap.Preferences.Preferences)
}

// NeedsReloadForEvent reports whether an appstate topic event announced a
// newer preferences revision that should be fetched.
func (p *PrefsState) NeedsReloadForEvent(ev client.AppStateEvent) bool {
	return ev.Type == client.EventAppStatePreferencesPut && ev.Revision > p.Revision
}
