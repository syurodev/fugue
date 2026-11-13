// Package motion holds the animation primitives of the Gio app: springs
// tuned by Spec (response and damping fraction, like the platform motion
// APIs designers know), the app's shared motion tokens (Snappy, Smooth,
// Bouncy, Exit), Presence for elements entering and leaving, Follower for
// values that chase a target such as a measured height, Slots for list items
// sliding to new places, and a per-frame Driver that asks for the next frame
// only while something still moves.
//
// Every animation is a spring, so retargeting one mid-way continues from its
// current value and velocity: an interrupted animation turns round instead
// of jumping. Everything is pure and reads time only from the values it is
// handed (in a window, gtx.Now), so animations are deterministic under test
// and an idle window schedules no frames.
//
// Owner: desktop app motion. Allowed callers: ui/cmd and ui/internal
// packages.
package motion
