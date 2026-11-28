//go:build linux

package folderpicker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

// The xdg-desktop-portal FileChooser (FUG-115): the desktop's own directory
// chooser, with zenity and kdialog as the CLI fallbacks the probe finds.
const (
	portalBusName         = "org.freedesktop.portal.Desktop"
	portalObjectPath      = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalFileChooserName = "org.freedesktop.portal.FileChooser"
	portalRequestName     = "org.freedesktop.portal.Request"
	portalOpenMethod      = portalFileChooserName + ".OpenFile"
	portalResponseSignal  = "Response"
	dbusIntrospectMethod  = "org.freedesktop.DBus.Introspectable.Introspect"
	dbusCallTimeout       = 2 * time.Second
	// responseCodeOK is the portal's success code; 1 is the user's cancel,
	// 2 any other failure.
	responseCodeOK        = 0
	responseCodeCancelled = 1
)

// The static causes behind a portal answer; they are wrapped, never
// constructed, at the call site.
var (
	errChooserNoHandle  = errors.New("the folder chooser's answer carries no request handle")
	errChooserUnusable  = errors.New("the folder chooser answered an unusable response")
	errChooserCode      = errors.New("the folder chooser answered an unreadable response code")
	errChooserRefused   = errors.New("the folder chooser refused")
	errChooserNoResults = errors.New("the folder chooser answered no results")
	errChooserNoFolder  = errors.New("the folder chooser answered no folder")
)

// portalProbe reports whether a session bus carries the portal's FileChooser
// interface. It runs once, on the picker's worker goroutine; a missing bus or
// interface falls the pick through to the CLI helpers.
func platformProbe(ctx context.Context) Availability {
	av := Availability{}
	_, zenityErr := exec.LookPath("zenity")
	av.Zenity = zenityErr == nil
	_, kdialogErr := exec.LookPath("kdialog")
	av.KDialog = kdialogErr == nil
	av.Portal = portalProbe(ctx)
	return av
}

// pickPortal runs the portal FileChooser in directory mode: it opens the
// desktop's chooser and answers the folder the user chose, or a cancel. The
// Response signal subscription goes out before OpenFile, so a fast answer
// cannot be missed. Seq numbers the request's handle token; uniqueness among
// outstanding requests is all the portal needs of it.
func pickPortal(ctx context.Context, seq *atomic.Uint64) (string, bool, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return "", false, fmt.Errorf("reach the desktop portal: %w", err)
	}
	defer closeDBusConn(conn)

	sigCh := make(chan *dbus.Signal, 4)
	conn.Signal(sigCh)
	defer conn.RemoveSignal(sigCh)
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchInterface(portalRequestName),
		dbus.WithMatchMember(portalResponseSignal),
	); err != nil {
		return "", false, fmt.Errorf("listen for the portal's answer: %w", err)
	}

	obj := conn.Object(portalBusName, portalObjectPath)
	opts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(fmt.Sprintf("fugue%d", seq.Add(1))),
		"directory":    dbus.MakeVariant(true),
		"modal":        dbus.MakeVariant(true),
	}
	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
	defer cancel()
	call := obj.CallWithContext(callCtx, portalOpenMethod, 0, "", opts)
	if callErr := call.Err; callErr != nil {
		return "", false, fmt.Errorf("open the desktop's folder chooser: %w", callErr)
	}
	if len(call.Body) != 1 {
		return "", false, fmt.Errorf("%w: no answer body", errChooserNoHandle)
	}
	handle, ok := call.Body[0].(dbus.ObjectPath)
	if !ok {
		return "", false, fmt.Errorf("%w: %T", errChooserNoHandle, call.Body[0])
	}

	for {
		select {
		case <-ctx.Done():
			return "", false, fmt.Errorf("the folder chooser did not answer: %w", ctx.Err())
		case sig, ok := <-sigCh:
			if !ok || sig == nil || sig.Path != handle {
				continue
			}
			return portalResponse(ctx, sig.Body)
		}
	}
}

// portalResponse decodes one portal Request::Response: the code, then the
// results whose uris name the chosen folder.
func portalResponse(ctx context.Context, body []any) (string, bool, error) {
	if len(body) < 2 {
		return "", false, fmt.Errorf("%w: %d fields", errChooserUnusable, len(body))
	}
	code, ok := body[0].(uint32)
	if !ok {
		return "", false, fmt.Errorf("%w: %T", errChooserCode, body[0])
	}
	switch code {
	case responseCodeCancelled:
		return "", true, nil
	case responseCodeOK:
	default:
		return "", false, fmt.Errorf("%w: code %d", errChooserRefused, code)
	}
	results, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return "", false, fmt.Errorf("%w: %T", errChooserNoResults, body[1])
	}
	urisV, ok := results["uris"]
	if !ok {
		return "", false, fmt.Errorf("%w: no uris field", errChooserNoFolder)
	}
	uris, ok := urisV.Value().([]string)
	if !ok || len(uris) == 0 {
		return "", false, fmt.Errorf("%w: %T", errChooserNoFolder, urisV.Value())
	}
	select {
	case <-ctx.Done():
		return "", false, fmt.Errorf("the folder chooser did not answer: %w", ctx.Err())
	default:
	}
	path, err := uriPath(uris[0])
	return path, false, err
}

// portalProbeOf answers whether a session bus carries the FileChooser
// interface, so the probe never promises a chooser the desktop cannot open.
func portalProbe(ctx context.Context) bool {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return false
	}
	defer closeDBusConn(conn)
	callCtx, cancel := context.WithTimeout(ctx, dbusCallTimeout)
	defer cancel()
	call := conn.Object(portalBusName, portalObjectPath).CallWithContext(callCtx, dbusIntrospectMethod, 0)
	if callErr := call.Err; callErr != nil || len(call.Body) != 1 {
		return false
	}
	xml, ok := call.Body[0].(string)
	if !ok {
		return false
	}
	return strings.Contains(xml, portalFileChooserName)
}

func closeDBusConn(conn *dbus.Conn) {
	if err := conn.Close(); err != nil {
		return
	}
}
