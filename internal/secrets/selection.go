package secrets

import (
	"cmp"
	"slices"
)

type candidate struct {
	finding  Finding
	priority uint8
}

func ranked(findings []Finding, priority uint8) []candidate {
	result := make([]candidate, 0, len(findings))
	for _, f := range findings {
		result = append(result, candidate{finding: f, priority: priority})
	}
	return result
}

func compareCandidates(a, b candidate) int {
	if order := cmp.Compare(a.finding.Start, b.finding.Start); order != 0 {
		return order
	}
	if order := cmp.Compare(b.finding.End, a.finding.End); order != 0 {
		return order
	}
	return cmp.Compare(b.priority, a.priority)
}

// Storage scales with actual matches, never the input length. Stable sorting
// preserves pattern order for equal spans (Anthropic before generic OpenAI).
func selectCandidates(candidates []candidate, allowed []Finding) []Finding {
	slices.SortStableFunc(candidates, compareCandidates)
	var selected []Finding
	var current candidate
	exception := 0
	for _, next := range candidates {
		f := next.finding
		for exception < len(allowed) && allowed[exception].End <= f.Start {
			exception++
		}
		if next.priority < 3 && exception < len(allowed) && allowed[exception].Start < f.End {
			continue
		}
		if current.finding.End <= current.finding.Start {
			current = next
			continue
		}
		if f.Start >= current.finding.End {
			selected = append(selected, current.finding)
			current = next
			continue
		}
		current.finding.End = max(current.finding.End, f.End)
		if next.priority > current.priority {
			current.finding.Kind, current.finding.Key = f.Kind, f.Key
			current.priority = next.priority
		}
	}
	if current.finding.End > current.finding.Start {
		selected = append(selected, current.finding)
	}
	return selected
}
