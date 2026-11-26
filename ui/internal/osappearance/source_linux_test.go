//go:build linux

package osappearance

import (
	"context"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

var errPortalUnavailable = errors.New("portal unavailable")

type fakePortalObject struct {
	dbus.BusObject
	calls []string
	fn    func(method string, args ...any) (dbus.Variant, error)
}

func (f *fakePortalObject) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	f.calls = append(f.calls, method)
	v, err := f.fn(method, args...)
	if err != nil {
		return &dbus.Call{Err: err}
	}
	return &dbus.Call{Body: []any{v}}
}

func TestLinuxDecodeColorScheme(t *testing.T) {
	t.Parallel()
	t.Run("freedesktop ReadOne uint32", assertDecodeFreedesktopReadOne)
	t.Run("freedesktop Read double-wrapped variant", assertDecodeFreedesktopDoubleVariant)
	t.Run("gnome color-scheme", assertDecodeGNOME)
	t.Run("SettingChanged filter", assertDecodeSettingChanged)
}

func assertDecodeFreedesktopReadOne(t *testing.T) {
	t.Helper()
	t.Parallel()
	for _, tc := range []struct {
		in   any
		want Appearance
	}{
		{dbus.MakeVariant(uint32(0)), Light},
		{dbus.MakeVariant(uint32(1)), Dark},
		{dbus.MakeVariant(uint32(2)), Light},
		{dbus.MakeVariant(uint32(9)), Unknown},
		{dbus.MakeVariant("1"), Unknown},
		{dbus.MakeVariant(int32(1)), Unknown},
		{dbus.MakeVariant(true), Unknown},
		{nil, Unknown},
	} {
		if got := decodeFreedesktopColorScheme(tc.in); got != tc.want {
			t.Errorf("decodeFreedesktopColorScheme(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func assertDecodeFreedesktopDoubleVariant(t *testing.T) {
	t.Helper()
	t.Parallel()
	for _, tc := range []struct {
		val  uint32
		want Appearance
	}{
		{0, Light},
		{1, Dark},
		{2, Light},
	} {
		wrapped := dbus.MakeVariant(dbus.MakeVariant(tc.val))
		if got := decodeFreedesktopColorScheme(wrapped); got != tc.want {
			t.Errorf("decodeFreedesktopColorScheme(double(%d)) = %v, want %v", tc.val, got, tc.want)
		}
	}
}

func assertDecodeGNOME(t *testing.T) {
	t.Helper()
	t.Parallel()
	for _, tc := range []struct {
		in   any
		want Appearance
	}{
		{dbus.MakeVariant("prefer-dark"), Dark},
		{dbus.MakeVariant(dbus.MakeVariant("prefer-dark")), Dark},
		{dbus.MakeVariant("default"), Light},
		{dbus.MakeVariant("prefer-light"), Light},
		{dbus.MakeVariant(uint32(1)), Unknown},
		{dbus.MakeVariant(false), Unknown},
		{nil, Unknown},
	} {
		if got := decodeGNOMEColorScheme(tc.in); got != tc.want {
			t.Errorf("decodeGNOMEColorScheme(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func assertDecodeSettingChanged(t *testing.T) {
	t.Helper()
	t.Parallel()
	for _, tc := range []struct {
		ns     string
		key    string
		val    any
		want   Appearance
		wantOK bool
	}{
		{freedesktopNamespace, colorSchemeKey, dbus.MakeVariant(uint32(1)), Dark, true},
		{gnomeNamespace, colorSchemeKey, dbus.MakeVariant("prefer-dark"), Dark, true},
		{"org.freedesktop.appearance", "accent-color", dbus.MakeVariant("blue"), Unknown, false},
		{"org.example.Other", colorSchemeKey, dbus.MakeVariant(uint32(1)), Unknown, false},
	} {
		if got, ok := decodeSettingChanged(tc.ns, tc.key, tc.val); ok != tc.wantOK || got != tc.want {
			t.Errorf("decodeSettingChanged(%q, %q) = (%v, %v), want (%v, %v)", tc.ns, tc.key, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestLinuxReadPortalFallback(t *testing.T) {
	t.Parallel()
	t.Run("ReadOne missing falls back to Read double variant", assertPortalFallbackReadOneToRead)
	t.Run("freedesktop missing falls back to gnome Read", assertPortalFallbackToGNOME)
	t.Run("all reads fail returns Unknown and error", assertPortalFallbackAllFail)
}

func assertPortalFallbackReadOneToRead(t *testing.T) {
	t.Helper()
	t.Parallel()
	obj := &fakePortalObject{
		fn: func(method string, args ...any) (dbus.Variant, error) {
			if method == portalSettingsIface+".ReadOne" {
				return dbus.Variant{}, dbus.Error{Name: dbusUnknownMethod}
			}
			if method == portalSettingsIface+".Read" && len(args) == 2 && args[0] == freedesktopNamespace {
				return dbus.MakeVariant(dbus.MakeVariant(uint32(1))), nil
			}
			return dbus.Variant{}, errPortalUnavailable
		},
	}
	got, hasFD, err := readPortalAppearance(t.Context(), obj)
	if err != nil || !hasFD || got != Dark {
		t.Fatalf("readPortalAppearance = (%v, %v, %v), want (dark, true, nil)", got, hasFD, err)
	}
}

func assertPortalFallbackToGNOME(t *testing.T) {
	t.Helper()
	t.Parallel()
	obj := &fakePortalObject{
		fn: func(method string, args ...any) (dbus.Variant, error) {
			if method == portalSettingsIface+".Read" && len(args) == 2 && args[0] == gnomeNamespace {
				return dbus.MakeVariant(dbus.MakeVariant("prefer-dark")), nil
			}
			return dbus.Variant{}, errPortalUnavailable
		},
	}
	got, hasFD, err := readPortalAppearance(t.Context(), obj)
	if err != nil || hasFD || got != Dark {
		t.Fatalf("readPortalAppearance = (%v, %v, %v), want (dark, false, nil)", got, hasFD, err)
	}
}

func assertPortalFallbackAllFail(t *testing.T) {
	t.Helper()
	t.Parallel()
	obj := &fakePortalObject{
		fn: func(string, ...any) (dbus.Variant, error) {
			return dbus.Variant{}, errPortalUnavailable
		},
	}
	got, hasFD, err := readPortalAppearance(t.Context(), obj)
	if err == nil || hasFD || got != Unknown {
		t.Fatalf("readPortalAppearance = (%v, %v, %v), want (unknown, false, err)", got, hasFD, err)
	}
}

func TestLinuxHandleSignal(t *testing.T) {
	t.Parallel()
	s := &linuxSource{}
	var changes []Appearance
	onChange := func(a Appearance) { changes = append(changes, a) }

	hasFD := false
	for _, tc := range []struct {
		name     string
		body     []any
		wantFD   bool
		wantCur  Appearance
		wantChgs int
	}{
		{"gnome fallback", []any{gnomeNamespace, colorSchemeKey, dbus.MakeVariant("prefer-dark")}, false, Dark, 1},
		{"freedesktop overrides", []any{freedesktopNamespace, colorSchemeKey, dbus.MakeVariant(uint32(2))}, true, Light, 2},
		{"duplicate light ignored", []any{freedesktopNamespace, colorSchemeKey, dbus.MakeVariant(uint32(0))}, true, Light, 2},
		{"gnome ignored after freedesktop", []any{gnomeNamespace, colorSchemeKey, dbus.MakeVariant("prefer-dark")}, true, Light, 2},
	} {
		hasFD = s.handleSignal(&dbus.Signal{Body: tc.body}, hasFD, onChange)
		if hasFD != tc.wantFD || s.Current() != tc.wantCur || len(changes) != tc.wantChgs {
			t.Fatalf("%s: hasFD=%v cur=%v changes=%v, want (%v, %v, %d)",
				tc.name, hasFD, s.Current(), changes, tc.wantFD, tc.wantCur, tc.wantChgs)
		}
	}
}
