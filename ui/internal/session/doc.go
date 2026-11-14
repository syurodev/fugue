// Package session holds the read-side state of the app's screens (F10.4a):
// the sidebar's session list over the vendor-sessions topic, the per-session
// transcript models over the transcript and transcript-delta topics, and the
// fetcher that performs the typed reads of pkg/client off the window
// goroutine.
//
// The models are pure: they never import Gio and change only on the window
// goroutine, which applies every delivered update by hand. The fetcher owns
// the package's one goroutine — it runs the blocking reads and hands the
// results back over its results channel, with a stop path. Topic payloads
// arrive here already mapped by the host: JSON for vendor-sessions and
// protobuf for the transcript topics, decoded against the same messages the
// host wrote.
//
// Owner: F10.4a. Allowed callers: appwin and the transcript view of
// ui/internal; nothing outside ui/.
package session
