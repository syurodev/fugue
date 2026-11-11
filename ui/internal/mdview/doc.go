// Package mdview draws the immutable markdown model of F10.3a with Gio
// (desktop-app DA-03): bold through the font weight, code in the bundled
// JetBrains Mono, links with a dotted underline and a hover or focus
// popover, and code blocks on their own horizontally scrolling surface. It
// owns layout only: colors come from the theme package, and the renderer
// never opens a link — the popover's open button queues an OpenLink for the
// caller, which decides (F10.4 confirms before anything reaches the OS).
//
// Layout runs on the window goroutine. The text it shapes is the model's
// already-sanitized text, tabs expanded, so no tab reaches the shaper, and
// nothing here loads a network resource: links get a generic icon, never a
// favicon, and images show their alt text.
//
// Owner: F10.3b. Allowed callers: the transcript view of F10.4 and
// ui/cmd/mdpreview; nothing outside ui/.
package mdview
