package app

import (
	"context"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vendor"
)

// SessionTokenEnv names the variable that carries a session token into a
// vendor process and on to `fugue mcp-bridge`.
const SessionTokenEnv = vendor.SessionTokenEnv

// VendorBridge locates `fugue mcp-bridge` for vendor sessions.
type VendorBridge = vendor.Bridge

// HostDeps are the ports StartHost needs.
type HostDeps struct {
	DataDir       string
	WorkspaceEnv  config.Env
	Config        HostConfig
	Clock         clock.Clock
	IDs           ids.Generator
	HostResources HostResources
	// DiskSpace reports the free disk space of the data directory's
	// filesystem for the §12 disk pause (F11.9). Nil uses the platform port.
	DiskSpace DiskSpaceChecker
	Transport HostTransport
	// VendorBinary overrides the Claude binary (absolute path); empty
	// resolves claude on PATH at probe time.
	VendorBinary string
	// CodexBinary overrides the Codex binary (absolute path); empty falls back
	// to VendorBinary when set or resolves codex on PATH at probe time.
	CodexBinary string
	// AgyBinary overrides the Antigravity binary (absolute path); empty falls back
	// to VendorBinary when set or resolves agy on PATH at probe time.
	AgyBinary string
	// VendorHostEnv is the environment snapshot vendor processes filter
	// through the vendor-runtime §8 allowlist.
	VendorHostEnv []string
	// Random mints the session tokens; nil reads the operating system's
	// cryptographic source.
	Random ids.Random
	// Bridge locates `fugue mcp-bridge` for vendor sessions: the fugue binary
	// and the host socket. Empty runs sessions without Fugue's MCP tools.
	Bridge VendorBridge
	// GitBinary overrides the git binary (absolute path) the team worktrees
	// use; empty locates git on the machine, and a missing git fails team
	// tasks that need a worktree closed (F11.3a).
	GitBinary string
	// TeamAdmitter decides when a queued team task may start. Nil uses the
	// local admitter (host capacity and the direct-child cap); the F11.6
	// scheduler plugs in here, and a test that wants the decide path alone
	// injects one that admits nothing.
	TeamAdmitter Admitter
	// TeamResultGate evaluates checks after fugue_task_done (Q-125). Nil fails
	// closed in F11.3 and leaves the task in checking until F11.5 wires checks.
	TeamResultGate ResultGate
	// TestFugueTools registers Fugue MCP tools for tests only. The fugue
	// binary never sets it, so a normal host serves none of them.
	TestFugueTools []*FugueTool
	// TestDelaySpawnRegistration, when set by tests, runs right before an
	// in-flight spawn registers its peer owner in VendorSessions.
	TestDelaySpawnRegistration func(context.Context)
	// TestOnWaitInflightSpawn, when set by tests, runs when OwnsPeer begins
	// waiting for in-flight spawn registrations to settle.
	TestOnWaitInflightSpawn func()
}
