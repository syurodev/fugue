// Package osappearance provides the desktop app's port for reading and watching
// the operating system's light or dark appearance preference.
//
// Logic packages such as appwin depend only on Source so tests can inject a
// deterministic fake without touching OS APIs or D-Bus.
//
// Owner: desktop app (ui module). Allowed callers: ui internal packages; the
// core module never imports this module.
package osappearance
