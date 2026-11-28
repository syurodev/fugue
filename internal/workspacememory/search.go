package workspacememory

import (
	"context"
	"slices"
	"strings"
)

// Search limits (§4) and Q-2 ranking constants.
const (
	DefaultSearchLimit = 10
	MaxSearchLimit     = 30
	ApproxSearchLimit  = 5

	q2WordWeight    = 0.65
	q2TrigramWeight = 0.35
	q2VerifiedBoost = 1.20
	q2FreshBoost    = 1.10
	q2StalePenalty  = 0.90
	q2NormBias      = 0.25
	q2NormScale     = 1.25
	maxApproxNgrams = 24
	maxSnippetRunes = 160
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

type rawSearchCandidate struct {
	row       entryRow
	wordScore float64
	triScore  float64
	invScore  float64
}

// Search runs weighted FTS5 word and trigram keyword search with approximate fallback.
func (s *Store) Search(ctx context.Context, in *SearchInput) (SearchResponse, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" {
		return SearchResponse{}, errInvalidArg("workspace_id is required")
	}
	if in.Kind != "" && !validKind(in.Kind) {
		return SearchResponse{}, errInvalidArg("invalid workspace memory kind filter")
	}
	limit := normalizeSearchLimit(in.Limit)
	wordQuery := sanitizeWordQuery(in.Query)
	triQuery := sanitizeTrigramQuery(in.Query)
	if wordQuery == "" && triQuery == "" {
		return SearchResponse{Results: []SearchResult{}}, nil
	}
	candidates, err := s.collectPrimaryCandidates(ctx, in, wordQuery, triQuery)
	if err != nil {
		return SearchResponse{}, err
	}
	if len(candidates) > 0 {
		results := s.rankAndHydrate(ctx, in.WorkspaceID, in.Query, candidates, false, limit)
		return SearchResponse{Results: results, Approximate: false}, nil
	}
	return s.runApproximateFallback(ctx, in, limit)
}

func normalizeSearchLimit(limit int) int {
	if limit <= 0 {
		return DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		return MaxSearchLimit
	}
	return limit
}

func (s *Store) collectPrimaryCandidates(
	ctx context.Context,
	in *SearchInput,
	wordQuery, triQuery string,
) (map[string]*rawSearchCandidate, error) {
	candidates := map[string]*rawSearchCandidate{}
	if wordQuery != "" {
		if err := s.queryWordFTS(ctx, in.WorkspaceID, in.Kind, wordQuery, candidates); err != nil {
			return nil, err
		}
	}
	if triQuery != "" {
		if err := s.queryTrigramFTS(ctx, in.WorkspaceID, in.Kind, triQuery, candidates); err != nil {
			return nil, err
		}
	}
	if in.IncludeInvalid {
		if err := s.collectInvalidMatches(ctx, in, candidates); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

func (s *Store) runApproximateFallback(ctx context.Context, in *SearchInput, limit int) (SearchResponse, error) {
	approxQuery := buildApproxTrigramQuery(in.Query)
	if approxQuery == "" {
		return SearchResponse{Results: []SearchResult{}}, nil
	}
	candidates := map[string]*rawSearchCandidate{}
	if err := s.queryTrigramFTS(ctx, in.WorkspaceID, in.Kind, approxQuery, candidates); err != nil {
		return SearchResponse{}, err
	}
	if len(candidates) == 0 {
		return SearchResponse{Results: []SearchResult{}}, nil
	}
	approxCap := min(limit, ApproxSearchLimit)
	results := s.rankAndHydrate(ctx, in.WorkspaceID, in.Query, candidates, true, approxCap)
	return SearchResponse{Results: results, Approximate: true}, nil
}

type scoredSearchResult struct {
	result      SearchResult
	updatedAtMs int64
}

func (s *Store) rankAndHydrate(
	ctx context.Context,
	workspaceID, rawQuery string,
	candidates map[string]*rawSearchCandidate,
	approximate bool,
	limit int,
) []SearchResult {
	items := make([]scoredSearchResult, 0, len(candidates))
	for _, cand := range candidates {
		_, freshness := s.evaluateFreshness(ctx, workspaceID, cand.row.anchors)
		score := computeQ2Score(cand, cand.row.verified, freshness)
		items = append(items, scoredSearchResult{
			result: SearchResult{
				ID:          cand.row.id,
				Label:       cand.row.label,
				Kind:        cand.row.kind,
				Summary:     cand.row.summary,
				Snippet:     buildMatchSnippet(&cand.row, rawQuery),
				Author:      cand.row.author,
				Verified:    cand.row.verified,
				Freshness:   freshness,
				Revision:    cand.row.revision,
				Approximate: approximate,
				Score:       score,
			},
			updatedAtMs: cand.row.updatedAtMs,
		})
	}
	sortScoredResults(items)
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]SearchResult, len(items))
	for i := range items {
		out[i] = items[i].result
	}
	return out
}

func sortScoredResults(items []scoredSearchResult) {
	slices.SortFunc(items, func(a, b scoredSearchResult) int {
		if a.result.Score != b.result.Score {
			if a.result.Score > b.result.Score {
				return -1
			}
			return 1
		}
		if a.result.Verified != b.result.Verified {
			if a.result.Verified {
				return -1
			}
			return 1
		}
		if a.updatedAtMs != b.updatedAtMs {
			if a.updatedAtMs > b.updatedAtMs {
				return -1
			}
			return 1
		}
		return strings.Compare(a.result.ID, b.result.ID)
	})
}

func computeQ2Score(cand *rawSearchCandidate, verified bool, freshness Freshness) float64 {
	base := q2WordWeight*cand.wordScore + q2TrigramWeight*cand.triScore + cand.invScore
	if verified {
		base *= q2VerifiedBoost
	}
	switch freshness {
	case FreshnessFresh:
		base *= q2FreshBoost
	case FreshnessStale:
		base *= q2StalePenalty
	case FreshnessUnknown:
	}
	return base
}

func normalizeBM25(raw float64) float64 {
	mag := -raw
	if mag < 0 {
		mag = 0
	}
	return (mag + q2NormBias) / (mag + q2NormScale)
}

func fallbackSnippet(summary, body string) string {
	text := strings.TrimSpace(summary)
	if text == "" {
		text = strings.TrimSpace(body)
	}
	runes := []rune(text)
	if len(runes) <= maxSnippetRunes {
		return text
	}
	return string(runes[:maxSnippetRunes]) + "..."
}
