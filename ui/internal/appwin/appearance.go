package appwin

// osAppearance is the resolved light/dark preference reported by the OS.
type osAppearance int

const (
	appearanceUnknown osAppearance = iota
	appearanceLight
	appearanceDark
)

// appearanceSource reads the OS light/dark preference and notifies on changes.
// Production builds inject the platform reader; tests inject a fake.
type appearanceSource interface {
	Current() osAppearance
	// Subscribe registers onChange to run when the OS appearance may have
	// changed, and returns a stop function that cancels the subscription and
	// stops any background watcher.
	Subscribe(onChange func()) (stop func())
}
