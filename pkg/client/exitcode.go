package client

// The exit codes `fugue serve` answers a refused start with, so a client that
// launched the host learns why it never came up (cli §5.2, §6). They are part
// of the public contract: the desktop app maps them onto its failure UX.
const (
	// ExitDataDirInUse means another process holds the data directory lock
	// (cli §6: `Data directory in use`, exit 8).
	ExitDataDirInUse = 8
	// ExitStoreUnsupported means the data directory holds a schema this
	// Fugue cannot serve; upgrading Fugue is the way out (activation §3).
	ExitStoreUnsupported = 9
)
