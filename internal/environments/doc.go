// Package environments provides an in-memory environment tag registry and
// confirmation policies for execution targets (roadmap F6.3b, interaction §3.4, I-03).
//
// Built-in tags include dev, stg, and prod. The prod tag is locked with
// requires_confirmation = true and cannot be modified or deleted. Empty tags
// and unknown tags fail closed by requiring confirmation.
//
// Policy revisions increment monotonically on every in-process update to a tag's
// confirmation policy, invalidating existing session grants even when a setting
// is changed and subsequently reverted.
//
// Owner: interaction (docs/adr/0002-vendor-domain.md, I-03, §3.4). Allowed callers:
// internal/interaction, internal/tools, internal/app, and test callers.
package environments
