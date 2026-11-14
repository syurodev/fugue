package app

import (
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
	DataDir      string
	WorkspaceEnv config.Env
	Config       HostConfig
	Clock        clock.Clock
	IDs          ids.Generator
	Transport    HostTransport
	// VendorBinary overrides the Claude binary (absolute path); empty
	// resolves claude on PATH at probe time.
	VendorBinary string
	// VendorHostEnv is the environment snapshot vendor processes filter
	// through the vendor-runtime §8 allowlist.
	VendorHostEnv []string
	// Random mints the session tokens; nil reads the operating system's
	// cryptographic source.
	Random ids.Random
	// Bridge locates `fugue mcp-bridge` for vendor sessions: the fugue binary
	// and the host socket. Empty runs sessions without Fugue's MCP tools.
	Bridge VendorBridge
	// TestFugueTools registers Fugue MCP tools for tests only. The fugue
	// binary never sets it, so a normal host serves none of them.
	TestFugueTools []*FugueTool
}
