package agy

import (
	"path/filepath"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

// Bridge wiring for the Fugue MCP tools (vendor-runtime §12). The bridge
// reaches agy as an `mcpServers` entry of session/new and session/load over
// the ACP pipe. The entry carries no env pairs on purpose: agy hands MCP
// server children its own process environment (measured on agy 1.5.9,
// 2026-11-16), which already carries the session token through the §8
// allowlist, so no token value is written to the pipe where agy could persist
// or log it. Measured on 1.5.9 and 2026-11-16 real sessions: nothing of the
// entry reaches agy's data directory.

// BridgeServerName is the MCP server name agy sees.
const BridgeServerName = "fugue"

// bridgeServer builds the mcpServers entry for the Fugue bridge. Both paths
// are host values, never client or profile input.
func bridgeServer(bridge vendor.Bridge) (McpServer, error) {
	if !filepath.IsAbs(bridge.Binary) || !filepath.IsAbs(bridge.Socket) {
		return McpServer{}, errcode.New(errcode.InvalidArgument, "agy: the mcp bridge needs absolute binary and socket paths")
	}
	return McpServer{
		Name:    BridgeServerName,
		Command: bridge.Binary,
		Args:    []string{"mcp-bridge", "--socket", bridge.Socket},
	}, nil
}

// bridgeServers builds the mcpServers list of session/new and session/load:
// the Fugue bridge when the host minted one, otherwise an empty list, which
// is what pre-F9.11 sessions sent.
func bridgeServers(bridge vendor.Bridge) ([]McpServer, error) {
	if bridge.Binary == "" {
		return []McpServer{}, nil
	}
	server, err := bridgeServer(bridge)
	if err != nil {
		return nil, err
	}
	return []McpServer{server}, nil
}
