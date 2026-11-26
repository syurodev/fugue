package osappearance

import "context"

// Appearance represents the resolved operating system color-scheme preference.
type Appearance int

const (
	// Unknown indicates the OS appearance could not be determined.
	Unknown Appearance = iota
	// Light indicates a light OS appearance (or no preference).
	Light
	// Dark indicates a dark OS appearance.
	Dark
)

// String returns a human-readable name for the appearance value.
func (a Appearance) String() string {
	switch a {
	case Unknown:
		return "unknown"
	case Light:
		return "light"
	case Dark:
		return "dark"
	default:
		return "unknown"
	}
}

func appearanceToCode(a Appearance) int32 {
	switch a {
	case Light:
		return 1
	case Dark:
		return 2
	case Unknown:
		return 0
	default:
		return 0
	}
}

func appearanceFromCode(code int32) Appearance {
	switch code {
	case 1:
		return Light
	case 2:
		return Dark
	default:
		return Unknown
	}
}

// Source reads and watches the OS appearance preference.
//
// Current returns the cached appearance without performing OS or IPC calls, so
// it is safe to invoke on every UI frame. Watch runs the background listener
// until ctx is canceled, invoking onChange whenever the cached appearance
// changes.
type Source interface {
	Current() Appearance
	Watch(ctx context.Context, onChange func(Appearance))
}
