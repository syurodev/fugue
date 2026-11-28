package workspacememory

import "context"

// SecretFinding describes a detected secret without carrying its raw value.
type SecretFinding struct {
	Kind string
	Key  string
}

// SecretScanner scans text for secrets before a memory write commits.
type SecretScanner interface {
	Scan(text string) []SecretFinding
}

// AnchorReader reads a workspace-confined file so the owner can compute and
// verify SHA-256 content hashes without importing internal/workspace.
type AnchorReader interface {
	ReadAnchor(ctx context.Context, workspaceID, rootName, relPath string) ([]byte, error)
}

// EventSink receives metadata-only write notifications after transaction commit.
type EventSink interface {
	OnMemoryEntryWritten(ctx context.Context, ev WrittenEvent)
}
