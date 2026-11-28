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

// AnchorHasher computes the lowercase hex SHA-256 digest of a workspace-confined
// file so the owner never holds raw file content (C-06).
type AnchorHasher interface {
	HashAnchor(ctx context.Context, workspaceID, rootName, relPath string) (string, error)
}

// EventSink receives metadata-only write notifications after transaction commit.
type EventSink interface {
	OnMemoryEntryWritten(ctx context.Context, ev WrittenEvent)
}
