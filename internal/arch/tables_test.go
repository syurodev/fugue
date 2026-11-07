package arch

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const migrationsDir = "internal/storage/migrations/"

var (
	createTable = regexp.MustCompile("(?is)CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?[\"`]?(\\w+)")
	sqlWord     = regexp.MustCompile(`(?i)\b(select|insert|update|delete|from|join|into|alter|drop)\b`)
)

func (r repo) owners() (map[string]string, []string, error) {
	owners := map[string]string{}
	var problems []string
	for _, file := range adrFiles() {
		rows, err := r.table(file, "| Table | Owner")
		if err != nil {
			return nil, nil, err
		}
		for _, cells := range rows {
			if len(cells) < 2 {
				return nil, nil, fmt.Errorf("%w: table ownership row %v is incomplete", errBadRow, cells)
			}
			if prev, dup := owners[cells[0]]; dup {
				problems = append(problems, fmt.Sprintf("table %s has two owner rows: %s and %s", cells[0], prev, cells[1]))
			}
			owners[cells[0]] = cells[1]
		}
	}
	return owners, problems, nil
}

// checkTableOwnership enforces quality-gates section 5 TestTableOwnership.
func checkTableOwnership(ctx context.Context, r repo) ([]string, error) {
	owners, out, err := r.owners()
	if err != nil {
		return nil, err
	}
	layers, _, err := r.layerTable()
	if err != nil {
		return nil, err
	}
	for table, owner := range owners {
		if _, ok := findRow(layers, owner); !ok {
			out = append(out, fmt.Sprintf("table %s: owner %s has no row in the layer table", table, owner))
		}
	}
	files, err := r.files(".go", ".sql")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		problems, fileErr := fileTableProblems(r, file, owners)
		if fileErr != nil {
			return nil, fileErr
		}
		out = append(out, problems...)
	}
	pkgProblems, err := sqlImportProblems(ctx, r, owners)
	if err != nil {
		return nil, err
	}
	out = append(out, pkgProblems...)
	slices.Sort(out)
	return out, nil
}

func fileTableProblems(r repo, file string, owners map[string]string) ([]string, error) {
	src, err := r.read(file)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range createTable.FindAllStringSubmatch(src, -1) {
		if !strings.HasPrefix(file, migrationsDir) || !strings.HasSuffix(file, ".sql") {
			out = append(out, fmt.Sprintf("%s: creates table %s outside %s*.sql", file, m[1], migrationsDir))
		}
		if _, ok := owners[m[1]]; !ok {
			out = append(out, fmt.Sprintf("%s: table %s has no owner row", file, m[1]))
		}
	}
	if !strings.HasSuffix(file, ".go") {
		return out, nil
	}
	touched, err := tablesTouchedByGo(r, file, owners)
	if err != nil {
		return nil, err
	}
	return append(out, touched...), nil
}

// tablesTouchedByGo finds SQL in string literals that names a table owned by a
// different package.
func tablesTouchedByGo(r repo, file string, owners map[string]string) ([]string, error) {
	f, err := r.parse(file)
	if err != nil {
		return nil, err
	}
	pkgDir := path.Dir(file)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil || !sqlWord.MatchString(text) {
			return true
		}
		for table, owner := range owners {
			if owner != pkgDir && regexp.MustCompile(`\b`+regexp.QuoteMeta(table)+`\b`).MatchString(text) {
				out = append(out, fmt.Sprintf("%s: SQL touches table %s, owned by %s", file, table, owner))
			}
		}
		return true
	})
	return out, nil
}

// sqlImportProblems keeps database/sql inside the packages that own tables and
// the SQLite driver inside the owner of schema_migrations.
func sqlImportProblems(ctx context.Context, r repo, owners map[string]string) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, owner := range owners {
		allowed[owner] = true
	}
	var out []string
	for _, p := range pkgs {
		for _, imp := range p.allImports() {
			switch {
			case imp == "database/sql" && !allowed[p.rel()]:
				out = append(out, p.rel()+" imports database/sql but owns no table")
			case imp == "modernc.org/sqlite" && p.rel() != owners["schema_migrations"]:
				out = append(out, fmt.Sprintf("%s imports the SQLite driver: only %s may", p.rel(), owners["schema_migrations"]))
			}
		}
	}
	return out, nil
}

func TestTableOwnership(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkTableOwnership(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkTableOwnership(t.Context(), fixture("tables-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkTableOwnership(t.Context(), fixture("tables-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"internal/rogue/schema.go: creates table stray outside internal/storage/migrations/*.sql",
			"internal/rogue/schema.go: table stray has no owner row",
			"internal/storage/migrations/0001_init.sql: table orphan has no owner row",
			"table exec_events has two owner rows: internal/execstore and internal/vendorstore",
			"table exec_runs: owner internal/ghost has no row in the layer table",
			"internal/coordinator/query.go: SQL touches table exec_events, owned by internal/vendorstore",
			"internal/coordinator imports database/sql but owns no table",
			"internal/coordinator imports the SQLite driver: only internal/storage may",
		)
	})
}
