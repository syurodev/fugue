//go:build linux

package osappearance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalBusName        = "org.freedesktop.portal.Desktop"
	portalObjectPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalSettingsIface  = "org.freedesktop.portal.Settings"
	freedesktopNamespace = "org.freedesktop.appearance"
	gnomeNamespace       = "org.gnome.desktop.interface"
	colorSchemeKey       = "color-scheme"
	signalSettingChanged = "SettingChanged"
	dbusUnknownMethod    = "org.freedesktop.DBus.Error.UnknownMethod"
	dbusCallTimeout      = 2 * time.Second
)

var errUnexpectedAppearanceValue = errors.New("unexpected color-scheme value")

type linuxSource struct {
	current atomic.Int32
	logOnce sync.Once
}

// New returns a Source backed by the XDG Desktop Portal Settings D-Bus service
// on Linux.
func New() Source {
	return &linuxSource{}
}

func (s *linuxSource) Current() Appearance {
	return appearanceFromCode(s.current.Load())
}

func (s *linuxSource) set(next Appearance, onChange func(Appearance)) {
	prev := appearanceFromCode(s.current.Swap(appearanceToCode(next)))
	if prev != next && onChange != nil {
		onChange(next)
	}
}

func (s *linuxSource) logUnavailable(err error) {
	s.logOnce.Do(func() {
		slog.Warn("os appearance unavailable; falling back to light", "error", err)
	})
}

func (s *linuxSource) Watch(ctx context.Context, onChange func(Appearance)) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		s.logUnavailable(err)
		<-ctx.Done()
		return
	}
	defer closeDBusConn(conn)

	sigCh := make(chan *dbus.Signal, 16)
	conn.Signal(sigCh)
	defer conn.RemoveSignal(sigCh)

	err = conn.AddMatchSignalContext(
		ctx,
		dbus.WithMatchObjectPath(portalObjectPath),
		dbus.WithMatchInterface(portalSettingsIface),
		dbus.WithMatchMember(signalSettingChanged),
	)
	if err != nil {
		s.logUnavailable(err)
		<-ctx.Done()
		return
	}

	obj := conn.Object(portalBusName, portalObjectPath)
	initial, hasFreedesktop, err := readPortalAppearance(ctx, obj)
	if err != nil {
		s.logUnavailable(err)
	} else if initial != Unknown {
		s.set(initial, onChange)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-sigCh:
			if !ok || sig == nil {
				return
			}
			hasFreedesktop = s.handleSignal(sig, hasFreedesktop, onChange)
		}
	}
}

func (s *linuxSource) handleSignal(sig *dbus.Signal, hasFreedesktop bool, onChange func(Appearance)) bool {
	if len(sig.Body) < 3 {
		return hasFreedesktop
	}
	ns, okNS := sig.Body[0].(string)
	key, okKey := sig.Body[1].(string)
	if !okNS || !okKey {
		return hasFreedesktop
	}
	next, matched := decodeSettingChanged(ns, key, sig.Body[2])
	if !matched {
		return hasFreedesktop
	}
	if ns == freedesktopNamespace {
		hasFreedesktop = true
	} else if hasFreedesktop {
		// Prefer org.freedesktop.appearance when available to avoid duplicate flips.
		return hasFreedesktop
	}
	if next != Unknown {
		s.set(next, onChange)
	}
	return hasFreedesktop
}

func closeDBusConn(conn *dbus.Conn) {
	if err := conn.Close(); err != nil {
		return
	}
}

func readPortalAppearance(ctx context.Context, obj dbus.BusObject) (app Appearance, hasFreedesktop bool, err error) {
	app, err = readFreedesktopAppearance(ctx, obj)
	if err == nil && app != Unknown {
		return app, true, nil
	}
	gnomeApp, gnomeErr := readGNOMEAppearance(ctx, obj)
	if gnomeErr == nil && gnomeApp != Unknown {
		return gnomeApp, false, nil
	}
	if err != nil {
		return Unknown, false, err
	}
	return Unknown, false, gnomeErr
}

func readFreedesktopAppearance(ctx context.Context, obj dbus.BusObject) (Appearance, error) {
	v, err := callPortalSetting(ctx, obj, "ReadOne", freedesktopNamespace, colorSchemeKey)
	if err != nil && isUnknownMethod(err) {
		v, err = callPortalSetting(ctx, obj, "Read", freedesktopNamespace, colorSchemeKey)
	}
	if err != nil {
		return Unknown, err
	}
	app := decodeFreedesktopColorScheme(v)
	if app == Unknown {
		return Unknown, errUnexpectedAppearanceValue
	}
	return app, nil
}

func readGNOMEAppearance(ctx context.Context, obj dbus.BusObject) (Appearance, error) {
	v, err := callPortalSetting(ctx, obj, "Read", gnomeNamespace, colorSchemeKey)
	if err != nil {
		return Unknown, err
	}
	app := decodeGNOMEColorScheme(v)
	if app == Unknown {
		return Unknown, errUnexpectedAppearanceValue
	}
	return app, nil
}

func callPortalSetting(ctx context.Context, obj dbus.BusObject, method, ns, key string) (dbus.Variant, error) {
	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
	defer cancel()
	var v dbus.Variant
	if err := obj.CallWithContext(callCtx, portalSettingsIface+"."+method, 0, ns, key).Store(&v); err != nil {
		return dbus.Variant{}, fmt.Errorf("dbus %s(%s, %s): %w", method, ns, key, err)
	}
	return v, nil
}

func isUnknownMethod(err error) bool {
	var dbusErr dbus.Error
	return errors.As(err, &dbusErr) && dbusErr.Name == dbusUnknownMethod
}

func unwrapVariant(v any) any {
	for range 4 {
		vr, ok := v.(dbus.Variant)
		if !ok {
			return v
		}
		v = vr.Value()
	}
	return v
}

func decodeFreedesktopColorScheme(v any) Appearance {
	val, ok := unwrapVariant(v).(uint32)
	if !ok {
		return Unknown
	}
	switch val {
	case 1:
		return Dark
	case 0, 2:
		return Light
	default:
		return Unknown
	}
}

func decodeGNOMEColorScheme(v any) Appearance {
	val, ok := unwrapVariant(v).(string)
	if !ok {
		return Unknown
	}
	if val == "prefer-dark" {
		return Dark
	}
	return Light
}

func decodeSettingChanged(ns, key string, val any) (Appearance, bool) {
	if key != colorSchemeKey {
		return Unknown, false
	}
	switch ns {
	case freedesktopNamespace:
		return decodeFreedesktopColorScheme(val), true
	case gnomeNamespace:
		return decodeGNOMEColorScheme(val), true
	default:
		return Unknown, false
	}
}
