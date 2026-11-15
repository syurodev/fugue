// Package glwin opens the headless GL windows of tests and the gallery's
// shot writer, and states the one rule that keeps them working under Mesa's
// software GL (Xvfb on the CI runners): a headless window is never released.
// Gio's Window.Release ends with eglTerminate on the process-wide default
// display, and the next eglInitialize after it fails there with "eglInitialize
// failed: 0x3001" — reproduced in a Linux container, every run, as soon as a
// process released one window and opened another, and the cause of the
// BAD_DISPLAY and BAD_CONTEXT losses once blamed on concurrency. Windows
// opened and kept until the process exits never see that: a second window
// only re-initializes an initialized display. Every test and command that
// renders headlessly opens its window through Open; nothing calls
// headless.NewWindow or Window.Release directly.
//
// Owner: F10.9. Allowed callers: ui tests and cmd/kitgallery.
package glwin
