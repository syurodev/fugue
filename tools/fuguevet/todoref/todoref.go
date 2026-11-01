package todoref

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/tools/go/analysis"
)

const (
	roadmapRel   = "docs/roadmap.md"
	statusColumn = 5
	statusDone   = "Done"
)

var (
	marker = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX)\b(?:\(([^)]*)\))?(:?)`)
	itemID = regexp.MustCompile(`^F\d+\.\d+$`)

	errNoRoadmap = errors.New("docs/roadmap.md not found; pass -todoref.roadmap")
)

// New returns the analyzer. The roadmap is found by walking up from the
// analyzed file, or taken from -todoref.roadmap.
func New() *analysis.Analyzer {
	r := &roadmap{}
	a := &analysis.Analyzer{
		Name:       "todoref",
		Doc:        "allow TODO only as TODO(F<phase>.<n>): ... naming a roadmap item that is not Done",
		Run:        r.run,
		ResultType: reflect.TypeFor[struct{}](),
	}
	a.Flags.StringVar(&r.path, "roadmap", "", "path to docs/roadmap.md (default: searched upwards from the file)")
	return a
}

// roadmap caches item statuses by roadmap path; analyzers run in parallel.
type roadmap struct {
	path string
	mu   sync.Mutex
	seen map[string]map[string]string
}

func (r *roadmap) run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		for _, group := range file.Comments {
			for _, c := range group.List {
				if err := r.checkComment(pass, file, c); err != nil {
					return nil, err
				}
			}
		}
	}
	return struct{}{}, nil
}

func (r *roadmap) checkComment(pass *analysis.Pass, file *ast.File, c *ast.Comment) error {
	matches := marker.FindAllStringSubmatch(c.Text, -1)
	if len(matches) == 0 {
		return nil
	}
	items, err := r.items(pass.Fset.File(file.Pos()).Name())
	if err != nil {
		return err
	}
	for _, m := range matches {
		if msg := judge(m, items); msg != "" {
			pass.Reportf(c.Pos(), "%s", msg)
		}
	}
	return nil
}

// judge returns the complaint for one marker, or "" when it is allowed.
func judge(m []string, items map[string]string) string {
	word, id, colon := m[1], m[2], m[3]
	switch {
	case word != "TODO":
		return word + " is not allowed: file a roadmap item and write TODO(F<phase>.<n>): ..."
	case !itemID.MatchString(id) || colon != ":":
		return "TODO must read TODO(F<phase>.<n>): ... with a roadmap item ID"
	}
	status, known := items[id]
	switch {
	case !known:
		return fmt.Sprintf("TODO(%s) names an item that is not in docs/roadmap.md", id)
	case status == statusDone:
		return fmt.Sprintf("TODO(%s) refers to an item that is already Done: do the work or name a live item", id)
	}
	return ""
}

func (r *roadmap) items(sourceFile string) (map[string]string, error) {
	path := r.path
	if path == "" {
		var err error
		if path, err = findUp(filepath.Dir(sourceFile)); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if items, ok := r.seen[path]; ok {
		return items, nil
	}
	items, err := parse(path)
	if err != nil {
		return nil, err
	}
	if r.seen == nil {
		r.seen = map[string]map[string]string{}
	}
	r.seen[path] = items
	return items, nil
}

func findUp(dir string) (string, error) {
	for {
		candidate := filepath.Join(dir, filepath.FromSlash(roadmapRel))
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoRoadmap
		}
		dir = parent
	}
}

// parse reads the work-item tables: rows whose first cell is an item ID such
// as F2.1, with the status in the sixth cell.
func parse(path string) (map[string]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open roadmap: %w", err)
	}
	items := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cells := strings.Split(strings.Trim(strings.TrimSpace(sc.Text()), "|"), "|")
		if len(cells) <= statusColumn {
			continue
		}
		if id := strings.TrimSpace(cells[0]); itemID.MatchString(id) {
			items[id] = strings.TrimSpace(cells[statusColumn])
		}
	}
	scanErr := sc.Err()
	if closeErr := f.Close(); closeErr != nil && scanErr == nil {
		scanErr = closeErr
	}
	if scanErr != nil {
		return nil, fmt.Errorf("read roadmap: %w", scanErr)
	}
	return items, nil
}
