package app

import (
	"context"
	"strings"
	"unicode"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// VendorCommandGroup is one command menu group, kept separate from transcript metadata.
type VendorCommandGroup struct {
	Name           string
	VendorCommands []string
	FugueCommands  []FugueCommand
}

// FugueCommand names a host command and the roadmap item that owns its behavior.
type FugueCommand struct {
	Name, Owner string
	Available   bool
}

// Commands returns the current vendor list and the host command registry.
func (s *VendorService) Commands(ctx context.Context, principal, sessionID string) ([]VendorCommandGroup, uint64, error) {
	if err := authorizeVendor(principal); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, VendorUnaryLimit)
	defer cancel()
	commands, err := s.sessions.SlashCommands(ctx, sessionID)
	if err != nil {
		return nil, 0, publicVendorError(err)
	}
	revision, err := s.store.CommandRevision(ctx, sessionID)
	if err != nil {
		return nil, 0, publicVendorError(err)
	}
	if revision < 0 {
		return nil, 0, errcode.New(errcode.Internal, "the vendor command revision is invalid")
	}
	return []VendorCommandGroup{
		{Name: "vendor", VendorCommands: commands},
		{Name: "fugue", FugueCommands: fugueCommandRegistry()},
	}, uint64(revision), nil
}

func fugueCommandRegistry() []FugueCommand {
	return []FugueCommand{
		{Name: "/fugue:team", Owner: "F11.2"},
		{Name: "/fugue:solo", Owner: "F11.2"},
		{Name: "/fugue:account", Owner: "F9.10"},
	}
}

func dispatchFugueCommand(text string) (registered, available bool) {
	command := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(command, "/fugue:") {
		return false, false
	}
	for _, entry := range fugueCommandRegistry() {
		if command == entry.Name {
			return true, entry.Available
		}
	}
	return true, false
}

func unknownFugueCommand() error {
	return errcode.New(errcode.VendorUnknownCommand, "the /fugue: command is not available")
}

func fugueCommandError(text string) error {
	registered, available := dispatchFugueCommand(text)
	if !registered {
		return nil
	}
	if available {
		return errcode.New(errcode.VendorCommandUnavailable, "the /fugue: command is not available yet")
	}
	for _, command := range fugueCommandRegistry() {
		if strings.TrimLeftFunc(text, unicode.IsSpace) == command.Name {
			return errcode.New(errcode.VendorCommandUnavailable, "the /fugue: command is not available yet")
		}
	}
	return unknownFugueCommand()
}
