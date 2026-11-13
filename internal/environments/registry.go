package environments

import (
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The canonical built-in environment tag names (interaction §3.4).
const (
	TagDev  = "dev"
	TagStg  = "stg"
	TagProd = "prod"
)

type tagEntry struct {
	requiresConfirmation bool
	revision             uint64
	locked               bool
	builtIn              bool
}

// Registry manages execution target environment tags, confirmation policies
// and monotonic policy revisions (interaction §3.4, I-03).
//
// Registry is safe for concurrent use.
type Registry struct {
	mu   sync.RWMutex
	seq  uint64
	tags map[string]*tagEntry
}

// New creates a Registry initialized with built-in tags:
// - dev: requires_confirmation = false
// - stg: requires_confirmation = true
// - prod: requires_confirmation = true, locked = true (cannot be modified or deleted).
func New() *Registry {
	r := &Registry{
		seq:  1,
		tags: make(map[string]*tagEntry),
	}
	r.tags[TagDev] = &tagEntry{
		requiresConfirmation: false,
		revision:             1,
		locked:               false,
		builtIn:              true,
	}
	r.tags[TagStg] = &tagEntry{
		requiresConfirmation: true,
		revision:             1,
		locked:               false,
		builtIn:              true,
	}
	r.tags[TagProd] = &tagEntry{
		requiresConfirmation: true,
		revision:             1,
		locked:               true,
		builtIn:              true,
	}
	return r
}

// RequiresConfirmation reports whether the given environment tag requires user
// confirmation before execution. Empty tags and unknown/unregistered tags fail
// closed by returning true (interaction §3.4, I-03).
func (r *Registry) RequiresConfirmation(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		return true
	}
	tag, ok := r.tags[name]
	if !ok {
		return true
	}
	return tag.requiresConfirmation
}

// PolicyRevision returns the monotonic policy revision counter for the given tag.
// If the tag is unknown or empty, PolicyRevision returns 0. The counter increments
// upon every in-process policy update, ensuring grant keys are invalidated even
// when a tag's policy is changed and subsequently reverted (interaction §3.4, I-E03).
func (r *Registry) PolicyRevision(name string) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		return 0
	}
	tag, ok := r.tags[name]
	if !ok {
		return 0
	}
	return tag.revision
}

// Set defines or updates the confirmation policy for a tag. Attempting to modify
// the locked prod tag returns a PermissionDenied error. Setting a tag increments
// its policy revision counter monotonically.
func (r *Registry) Set(name string, requiresConfirmation bool) error {
	if name == "" {
		return errcode.New(errcode.InvalidArgument, "tag name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if tag, ok := r.tags[name]; ok {
		if tag.locked {
			return errcode.New(errcode.PermissionDenied, "cannot modify locked environment tag: "+name)
		}
		r.seq++
		tag.revision = r.seq
		tag.requiresConfirmation = requiresConfirmation
		return nil
	}
	r.seq++
	r.tags[name] = &tagEntry{
		requiresConfirmation: requiresConfirmation,
		revision:             r.seq,
		locked:               false,
		builtIn:              false,
	}
	return nil
}

// Delete removes a user-defined tag. Attempting to delete prod or built-in tags
// returns a PermissionDenied error.
func (r *Registry) Delete(name string) error {
	if name == "" {
		return errcode.New(errcode.InvalidArgument, "tag name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tag, ok := r.tags[name]
	if !ok {
		return nil
	}
	if tag.locked {
		return errcode.New(errcode.PermissionDenied, "cannot delete locked environment tag: "+name)
	}
	if tag.builtIn {
		return errcode.New(errcode.PermissionDenied, "cannot delete built-in environment tag: "+name)
	}
	delete(r.tags, name)
	return nil
}
