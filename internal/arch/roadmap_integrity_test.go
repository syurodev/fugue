package arch

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	roadmapDependsColumn         = 3
	roadmapIntegrityStatusColumn = 5
	roadmapDroppedStatus         = "Dropped"
)

var (
	roadmapItemID     = regexp.MustCompile(`F\d+\.\d+[a-z]?`)
	roadmapPhaseRange = regexp.MustCompile(`^F\d+–F\d+$`)
	roadmapItemRange  = regexp.MustCompile(`^(F\d+\.\d+[a-z]?)–(F\d+\.\d+[a-z]?)$`)
)

type roadmapItem struct {
	id      string
	depends string
	status  string
}

// checkRoadmapIntegrity reports repeated item IDs, missing dependencies and
// cycles in the dependency graph, including the dependencies of composite rows.
func checkRoadmapIntegrity(r repo) ([]string, error) {
	rows, err := r.table(roadmapFile, "| ID | Item | Design | Depends on |")
	if err != nil {
		return nil, err
	}
	items, byID, violations := roadmapItems(rows)
	graph, dependencyViolations := roadmapDependencyGraph(items, byID)
	violations = append(violations, dependencyViolations...)
	violations = append(violations, roadmapCycles(graph)...)
	slices.Sort(violations)
	return violations, nil
}

func roadmapItems(rows [][]string) ([]roadmapItem, map[string]roadmapItem, []string) {
	items := make([]roadmapItem, 0, len(rows))
	counts := make(map[string]int)
	for _, row := range rows {
		if len(row) <= roadmapIntegrityStatusColumn || !roadmapItemID.MatchString(row[0]) || roadmapItemID.FindString(row[0]) != row[0] {
			continue
		}
		item := roadmapItem{id: row[0], depends: row[roadmapDependsColumn], status: row[roadmapIntegrityStatusColumn]}
		items = append(items, item)
		counts[item.id]++
	}

	var violations []string
	for id, count := range counts {
		if count > 1 {
			violations = append(violations, fmt.Sprintf("%s appears on %d rows", id, count))
		}
	}

	byID := make(map[string]roadmapItem, len(items))
	for _, item := range items {
		byID[item.id] = item
	}
	return items, byID, violations
}

func roadmapDependencyGraph(items []roadmapItem, byID map[string]roadmapItem) (map[string][]string, []string) {
	graph := make(map[string][]string)
	var violations []string
	for _, item := range items {
		deps, missing := roadmapDependencies(item, byID)
		violations = append(violations, missing...)
		graph[item.id] = append(deps, compositeChildren(item, byID)...)
	}
	return graph, violations
}

func roadmapDependencies(item roadmapItem, byID map[string]roadmapItem) ([]string, []string) {
	var deps, violations []string
	for _, ref := range dependencyReferences(item.depends) {
		if roadmapPhaseRange.MatchString(ref) {
			continue
		}
		targets, isRange := expandRoadmapRange(ref, byID)
		if !isRange {
			targets = []string{ref}
		}
		for _, target := range targets {
			targetItem, exists := byID[target]
			if !exists {
				violations = append(violations, fmt.Sprintf("%s depends on missing %s", item.id, target))
				continue
			}
			if item.status != roadmapDroppedStatus && targetItem.status != roadmapDroppedStatus {
				deps = append(deps, target)
			}
		}
	}
	return deps, violations
}

func compositeChildren(item roadmapItem, byID map[string]roadmapItem) []string {
	if item.status == roadmapDroppedStatus {
		return nil
	}
	var children []string
	for id, child := range byID {
		if child.status != roadmapDroppedStatus && strings.HasPrefix(id, item.id) && len(id) == len(item.id)+1 && id[len(item.id)] >= 'a' && id[len(item.id)] <= 'z' {
			children = append(children, id)
		}
	}
	slices.Sort(children)
	return children
}

// dependencyReferences groups item ranges as one token and leaves phase ranges
// intact so callers can explicitly ignore them.
func dependencyReferences(cell string) []string {
	var refs []string
	for _, token := range strings.FieldsFunc(cell, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	}) {
		if strings.Contains(token, "–") {
			refs = append(refs, token)
		} else {
			refs = append(refs, roadmapItemID.FindAllString(token, -1)...)
		}
	}
	return refs
}

// expandRoadmapRange expands a same-phase minor-version range to existing IDs.
func expandRoadmapRange(ref string, byID map[string]roadmapItem) ([]string, bool) {
	match := roadmapItemRange.FindStringSubmatch(ref)
	if match == nil {
		return nil, false
	}
	left, right := strings.Split(strings.TrimPrefix(match[1], "F"), "."), strings.Split(strings.TrimPrefix(match[2], "F"), ".")
	if left[0] != right[0] {
		return nil, false
	}
	start, errStart := strconv.Atoi(left[1])
	end, errEnd := strconv.Atoi(right[1])
	if errStart != nil || errEnd != nil || start > end {
		return nil, false
	}
	var ids []string
	for id := range byID {
		parts := strings.Split(strings.TrimPrefix(id, "F"), ".")
		minor, err := strconv.Atoi(strings.TrimRight(parts[1], "abcdefghijklmnopqrstuvwxyz"))
		if err == nil && parts[0] == left[0] && minor >= start && minor <= end {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, true
}

func roadmapCycles(graph map[string][]string) []string {
	for node := range graph {
		slices.Sort(graph[node])
	}
	state := make(map[string]uint8)
	var stack []string
	var cycles []string
	var visit func(string)
	visit = func(node string) {
		state[node] = 1
		stack = append(stack, node)
		for _, next := range graph[node] {
			switch state[next] {
			case 0:
				visit(next)
			case 1:
				start := slices.Index(stack, next)
				path := append(slices.Clone(stack[start:]), next)
				cycles = append(cycles, formatRoadmapCycle(path))
			}
		}
		stack = stack[:len(stack)-1]
		state[node] = 2
	}
	nodes := make([]string, 0, len(graph))
	for node := range graph {
		nodes = append(nodes, node)
	}
	slices.Sort(nodes)
	for _, node := range nodes {
		if state[node] == 0 {
			visit(node)
		}
	}
	return cycles
}

func formatRoadmapCycle(path []string) string {
	cycle := path[:len(path)-1]
	start := 0
	for i := 1; i < len(cycle); i++ {
		if cycle[i] < cycle[start] {
			start = i
		}
	}
	rotated := append(slices.Clone(cycle[start:]), cycle[:start]...)
	rotated = append(rotated, rotated[0])
	return "cycle: " + strings.Join(rotated, " -> ")
}

func TestRoadmapIntegrity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		repo repo
		want []string
	}{
		{name: "repository", repo: repo{root: repoRoot}},
		{name: "clean fixture", repo: fixture("roadmap-integrity-good")},
		{name: "violations", repo: fixture("roadmap-integrity-bad"), want: []string{
			"F9.6 appears on 2 rows",
			"F1.1 depends on missing F99.1",
			"cycle: F5.6 -> F6.3 -> F6.3b -> F5.6",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkRoadmapIntegrity(tc.repo)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.want) > 0 {
				expectViolations(t, got, tc.want...)
			} else {
				expectNone(t, got)
			}
		})
	}
}
