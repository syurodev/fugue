package appwin

import (
	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// effortDefaultID lets Select treat "vendor default" as selectable instead of no
// value; effortValue maps it back to "" for the host profile.
const (
	effortDefaultID    = "default"
	labelVendorDefault = "vendor default"
)

// effortOptions are the effort values the host's profile allowlist accepts;
// empty uses the vendor's default and the host rejects anything else. The
// host validates the chosen value, so the picker offers exactly these.
func effortOptions() []kit.SelectOption {
	return []kit.SelectOption{
		{ID: effortDefaultID, Label: labelVendorDefault},
		{ID: "low", Label: "low"},
		{ID: "medium", Label: "medium"},
		{ID: "high", Label: "high"},
		{ID: "xhigh", Label: "xhigh"},
		{ID: "max", Label: "max"},
	}
}

// workspaceItems maps the shared workspace model onto the combobox's rows.
func workspaceItems(list []workspace.Summary) []kit.SuggestItem {
	items := make([]kit.SuggestItem, 0, len(list))
	for _, ws := range list {
		items = append(items, kit.SuggestItem{ID: ws.ID, Label: ws.Name})
	}
	return items
}

// rootOptions maps the workspace's usable roots onto the select's options.
func rootOptions(roots []workspace.Root) []kit.SelectOption {
	opts := make([]kit.SelectOption, 0, len(roots))
	for _, r := range roots {
		opts = append(opts, kit.SelectOption{ID: r.Name, Label: r.Name})
	}
	return opts
}

// vendorOptions names the probed vendors, Claude always — the stage-A vendor
// and the one a probe without answers still serves.
func vendorOptions(c *composer) []kit.SelectOption {
	var opts []kit.SelectOption
	if _, ok := c.caps[client.VendorClaude]; !ok {
		opts = append(opts, kit.SelectOption{ID: client.VendorClaude, Label: client.VendorClaude})
	}
	for vendor := range c.caps {
		opts = append(opts, kit.SelectOption{ID: vendor, Label: vendor})
	}
	return opts
}
