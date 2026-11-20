package session

import (
	"strings"

	"github.com/syurodev/fugue/pkg/client"
)

// Commands is one session's slash commands (vendor-runtime §9): the vendor's
// own commands in vendor order and the host's `/fugue:` registry, refreshed
// whenever the session's commands revision advances on the vendor-sessions
// topic. It changes only on the window goroutine.
type Commands struct {
	// Revision is the commands revision the list was read at; a list older
	// than the one held is stale and ignored.
	Revision uint64
	vendor   []string
	fugue    []client.FugueCommand
}

// ApplyList replaces the commands with a ListCommands read. A read older
// than the revision held is stale and ignored: the topic announced a newer
// list than the one this read answered.
func (c *Commands) ApplyList(list client.Commands) {
	if list.Revision < c.Revision {
		return
	}
	c.Revision = list.Revision
	c.vendor, c.fugue = nil, nil
	for _, g := range list.Groups {
		if g.Name == fugueGroup {
			c.fugue = append(c.fugue, g.Fugue...)
			continue
		}
		c.vendor = append(c.vendor, g.Vendor...)
	}
}

// Vendor returns the vendor's own command names in vendor order.
func (c *Commands) Vendor() []string { return c.vendor }

// Fugue returns the host's `/fugue:` commands in registry order.
func (c *Commands) Fugue() []client.FugueCommand { return c.fugue }

// Suggestion is one row of the composer's slash menu.
type Suggestion struct {
	// ID is the command name, which becomes the whole prompt when chosen
	// (vendor-runtime §9: a command is the entire prompt).
	ID string
	// Secondary names the row's group.
	Secondary string
	// Disabled marks a `/fugue:` command the host registered but does not
	// currently offer; the host would reject the prompt.
	Disabled bool
}

const (
	// fugueGroup is the name the host gives its `/fugue:` group in the
	// ListCommands answer, and vendorGroup the vendor's own group.
	fugueGroup  = "fugue"
	vendorGroup = "vendor"
)

// SlashQuery reports whether the draft names a slash command and the command
// name typed so far: the draft's first word without its leading "/" —
// leading whitespace is ignored, as the host ignores it (vendor-runtime §9).
// The "/" alone names nothing yet and suggests everything. A draft whose
// first word is complete (whitespace follows it) is no longer naming a
// command: the name is closed and the menu has nothing to suggest.
func SlashQuery(draft string) (string, bool) {
	trimmed := strings.TrimLeft(draft, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	name := trimmed[1:]
	if _, hasSpace := cutFirstWord(name); hasSpace {
		return "", false
	}
	return name, true
}

// cutFirstWord splits s at its first space or tab.
func cutFirstWord(s string) (head string, cut bool) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], true
	}
	return s, false
}

// Suggest ranks the slash suggestions for the query typed after the "/":
// names starting with the query come first, names containing it follow, and
// vendor order — then registry order — is preserved within each rank
// (vendor-runtime §9). The vendor group comes before the `/fugue:` group. A
// suggestion's id is the whole prompt the choice makes: the vendor's names
// arrive without the leading "/", the `/fugue:` registry carries its own. An
// empty query suggests everything. Folding is the caller's business: the
// vendor names the commands as the vendor spelled them.
func (c *Commands) Suggest(query string) []Suggestion {
	var vendorItems, fugueItems []Suggestion
	vendorItems = rankCommands(query, c.vendor, func(name string) Suggestion {
		return Suggestion{ID: "/" + name, Secondary: vendorGroup}
	})
	for _, fc := range c.fugue {
		if !matchCommand(query, strings.TrimPrefix(fc.Name, "/")) {
			continue
		}
		fugueItems = append(fugueItems, Suggestion{
			ID: fc.Name, Secondary: fc.Owner, Disabled: !fc.Available,
		})
	}
	return append(vendorItems, fugueItems...)
}

// rankCommands keeps the vendor's order within each rank.
func rankCommands(query string, names []string, row func(string) Suggestion) []Suggestion {
	var prefix, sub []Suggestion
	for _, name := range names {
		if !matchCommand(query, name) {
			continue
		}
		if strings.HasPrefix(name, query) {
			prefix = append(prefix, row(name))
			continue
		}
		sub = append(sub, row(name))
	}
	return append(prefix, sub...)
}

// matchCommand reports whether the command name matches the query: an empty
// query matches everything, otherwise the query must appear in the name.
func matchCommand(query, name string) bool {
	return query == "" || strings.Contains(name, query)
}
