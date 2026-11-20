package session

import (
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

// listCommands builds a Commands model over a ListCommands read: the vendor
// group in vendor order and the `/fugue:` group with one unavailable command.
func listCommands(t *testing.T) *Commands {
	t.Helper()
	var cmds Commands
	cmds.ApplyList(client.Commands{Revision: 3, Groups: []client.CommandGroup{
		{Name: vendorGroup, Vendor: []string{"clear", "compact"}},
		{Name: "fugue", Fugue: []client.FugueCommand{
			{Name: "/fugue:team", Owner: "F11.2"},
			{Name: "/fugue:account", Owner: "F9.10", Available: true},
		}},
	}})
	return &cmds
}

// TestCommandsModelSuggestsPrefixFirstInVendorOrder pins the §9 rule: names
// starting with the typed string come first, names containing it follow, and
// the vendor's order is preserved within each rank; the `/fugue:` group comes
// after the vendor group. A command is the whole prompt, so the suggestion's
// id is the command name itself.
func TestCommandsModelSuggestsPrefixFirstInVendorOrder(t *testing.T) {
	t.Parallel()
	var cmds Commands
	cmds.ApplyList(client.Commands{Revision: 1, Groups: []client.CommandGroup{
		{Name: vendorGroup, Vendor: []string{"compact", "clear", "config", "recount"}},
	}})
	got := cmds.Suggest("co")
	// A suggestion's id is the whole prompt the choice makes (§9): the
	// vendor's names arrive without the leading slash.
	want := []Suggestion{
		{ID: "/compact", Secondary: vendorGroup},
		{ID: "/config", Secondary: vendorGroup},
		{ID: "/recount", Secondary: vendorGroup},
	}
	if len(got) != len(want) {
		t.Fatalf("suggestions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestCommandsModelOrdersTheGroupsAndMarksAvailability pins the two-group
// menu: the vendor group first, the `/fugue:` group after it, and a
// registered but unavailable `/fugue:` command is disabled — the host would
// reject the prompt, and it must never reach the vendor (VR-10).
func TestCommandsModelOrdersTheGroupsAndMarksAvailability(t *testing.T) {
	t.Parallel()
	cmds := listCommands(t)
	got := cmds.Suggest("")
	if len(got) != 4 {
		t.Fatalf("suggestions = %+v, want the two groups' four commands", got)
	}
	if got[0].ID != "/clear" || got[1].ID != "/compact" {
		t.Fatalf("vendor order = %v, %v; want the vendor's own order", got[0].ID, got[1].ID)
	}
	if got[2].ID != "/fugue:team" || !got[2].Disabled {
		t.Fatalf("unavailable fugue command = %+v, want it disabled", got[2])
	}
	if got[3].ID != "/fugue:account" || got[3].Disabled {
		t.Fatalf("available fugue command = %+v, want it offered", got[3])
	}
}

// TestCommandsModelIgnoresAStaleList pins the revision rule: a read older
// than the revision held is dropped — the topic announced a newer list than
// the one this read answered.
func TestCommandsModelIgnoresAStaleList(t *testing.T) {
	t.Parallel()
	cmds := listCommands(t)
	cmds.ApplyList(client.Commands{Revision: 2, Groups: []client.CommandGroup{
		{Name: vendorGroup, Vendor: []string{"stale"}},
	}})
	if cmds.Revision != 3 || len(cmds.Vendor()) != 2 {
		t.Fatalf("commands after a stale read = revision %d vendor %v; want revision 3 and the newer list", cmds.Revision, cmds.Vendor())
	}
}

// TestSlashQueryCutsTheCommandName pins the draft rule: the first word after
// the leading "/" names the command, leading whitespace is ignored the way
// the host ignores it, whitespace closes the name, and anything that is not
// a slash command suggests nothing.
func TestSlashQueryCutsTheCommandName(t *testing.T) {
	t.Parallel()
	for tc, want := range map[string]struct {
		query string
		ok    bool
	}{
		"/comp":       {"comp", true},
		"  /comp":     {"comp", true},
		"/":           {"", true},
		"/comp  ":     {"", false},
		"/clear now":  {"", false},
		"hello /comp": {"", false},
		"hello":       {"", false},
		"":            {"", false},
	} {
		query, ok := SlashQuery(tc)
		if ok != want.ok || query != want.query {
			t.Fatalf("SlashQuery(%q) = %q, %v; want %q, %v", tc, query, ok, want.query, want.ok)
		}
	}
}
