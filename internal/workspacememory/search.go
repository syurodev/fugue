package workspacememory

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Search limits (§4).
const (
	DefaultSearchLimit = 10
	MaxSearchLimit     = 30
)

// SearchInput specifies keyword search parameters for a single workspace.
type SearchInput struct {
	WorkspaceID    string
	Query          string
	Kind           Kind
	IncludeInvalid bool
	Limit          int
}

// SearchResult is one ranked memory match returned by Search.
type SearchResult struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Kind        Kind      `json:"kind"`
	Summary     string    `json:"summary"`
	Snippet     string    `json:"snippet"`
	Author      Author    `json:"author"`
	Verified    bool      `json:"verified"`
	Freshness   Freshness `json:"freshness"`
	Revision    uint64    `json:"revision"`
	Approximate bool      `json:"approximate"`
	Score       float64   `json:"score"`
}

// SearchResponse holds ranked matches and whether approximate fallback was used.
type SearchResponse struct {
	Results     []SearchResult `json:"results"`
	Approximate bool           `json:"approximate"`
}

// Search runs weighted FTS5 word and trigram keyword search with approximate fallback.
func (s *Store) Search(_ context.Context, _ *SearchInput) (SearchResponse, error) {
	return SearchResponse{}, errcode.New(errcode.Unavailable, "workspace memory search is pending")
}
