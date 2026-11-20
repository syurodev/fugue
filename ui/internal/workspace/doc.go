// Package workspace is the app's workspace state (F10.14): the summaries the
// `workspaces` topic reports, the detail reads fold into (roots, the
// compare-and-swap hash, a pending policy change's diff), and the mutation
// worker that posts create, root and delete changes off the window goroutine.
// The sidebar groups sessions under the workspaces this model holds.
//
// Owner: F10.14. Allowed callers: ui/internal/appwin only; everything changes
// on the window goroutine, which applies deliveries and results here. The
// host side is internal/app's WorkspaceService (F5.6c); an out-of-band policy
// change waits for the user's confirmation in the app (Q-64).
package workspace
