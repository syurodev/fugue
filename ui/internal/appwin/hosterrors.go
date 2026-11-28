package appwin

import (
	"errors"
	"strings"

	"github.com/syurodev/fugue/pkg/client"
)

// hostErrorMessage maps Host API error codes onto user-facing messages while
// never echoing entry or secret content.
func hostErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, client.ErrDisconnected) {
		return "Disconnected from the Fugue host. Reconnect and try again."
	}
	if errors.Is(err, client.ErrMemoryImportFileTooLarge) {
		return "Import file exceeds the 32 MiB limit."
	}
	if code, ok := client.MemoryCodeOf(err); ok && code != "" {
		switch code {
		case client.MemoryCodeRevisionConflict:
			return "This entry changed — reloaded the latest version."
		case client.MemoryCodeContainsSecret:
			return "Rejected: entry appears to contain a secret."
		case client.MemoryCodeLabelTaken:
			return "That label is already used by another valid entry in this workspace."
		case client.MemoryCodeTooLarge:
			return "Entry exceeds the allowed size or keyword count limit."
		case client.MemoryCodeLanguage:
			return "Entry prose must be primarily English (C-08)."
		case client.MemoryCodeRateLimited:
			return "Memory write rate limit exceeded. Wait a moment and try again."
		case client.MemoryCodeLimitReached:
			return "Workspace memory has reached its maximum number of valid entries."
		case client.MemoryCodeImportInvalid:
			return "The selected file is not a valid Fugue memory Markdown export."
		case client.MemoryCodeImportStale:
			return "memory changed, preview again"
		}
	}
	msg := err.Error()
	if code, ok := client.WorkspaceCodeOf(err); ok && code != "" {
		if _, after, found := strings.Cut(msg, code+": "); found {
			return strings.TrimSpace(after)
		}
	}
	return msg
}
