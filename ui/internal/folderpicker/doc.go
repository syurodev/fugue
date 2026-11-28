// Package folderpicker picks a folder with the desktop's native chooser
// (FUG-115): macOS osascript choose folder, and on Linux the xdg-desktop-portal
// FileChooser in directory mode, then zenity --file-selection --directory,
// then kdialog --getexistingdirectory. The pick runs off the UI goroutine in
// the shape of the workspace mutator: one worker takes requests, runs the
// blocking helper, and hands results back for the window goroutine's frame
// loop. A cancelled pick is a no-op, and the picked path is data — the host
// still enforces every workspace root rule.
//
// Owner: FUG-115. Allowed callers: ui/internal/appwin only.
package folderpicker
