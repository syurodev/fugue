//go:build !darwin

package appwin

// newPlatformAppearanceSource returns appearanceUnknown until a Linux D-Bus
// client (github.com/godbus/dbus/v5) is approved in docs/dependencies.md.
func newPlatformAppearanceSource() appearanceSource {
	return unknownAppearanceSource{}
}

type unknownAppearanceSource struct{}

func (unknownAppearanceSource) Current() osAppearance { return appearanceUnknown }

func (unknownAppearanceSource) Subscribe(func()) func() { return func() {} }
