package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	modulePrefix   = "github.com/syurodev/fugue/"
	strictFloor    = 90.0
	defaultFloor   = 80.0
	baselineSlack  = 0.5
	generatedTree  = "internal/hostapi/internal/pb"
	baselineIndent = "  "
	profileFields  = 3
)

// Errors reported by the coverage gate.
var (
	ErrCoverage        = errors.New("coverage gate failed")
	ErrProfileSyntax   = errors.New("malformed coverage profile")
	ErrBaselineInvalid = errors.New("malformed coverage baseline")
)

type packageStats struct {
	statements int
	covered    int
}

func (s packageStats) percent() float64 {
	return 100 * float64(s.covered) / float64(s.statements)
}

func runCoverage(args []string, stdout io.Writer) error {
	fs := newFlags("coverage")
	profile := fs.String("profile", "cover.out", "coverage profile written by go test -coverprofile")
	baselinePath := fs.String("baseline", "quality/coverage.json", "per-package baseline")
	update := fs.Bool("update", false, "raise baselines to the measured values (never lowers)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	stats, err := readProfile(*profile)
	if err != nil {
		return err
	}
	baseline, err := readBaseline(*baselinePath)
	if err != nil {
		return err
	}
	if problems := evaluate(stats, baseline); len(problems) > 0 {
		return fmt.Errorf("%w:\n%s", ErrCoverage, strings.Join(problems, "\n"))
	}
	if *update {
		return writeBaseline(*baselinePath, raised(stats, baseline))
	}
	return say(stdout, "coverage ok: %d packages", len(stats))
}

func readProfile(name string) (map[string]packageStats, error) {
	f, err := os.Open(filepath.Clean(name))
	if err != nil {
		return nil, fmt.Errorf("open profile: %w", err)
	}
	stats, parseErr := parseProfile(f)
	closeErr := f.Close()
	if parseErr != nil {
		return nil, parseErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close profile: %w", closeErr)
	}
	return stats, nil
}

func parseProfile(r io.Reader) (map[string]packageStats, error) {
	stats := map[string]packageStats{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		pkg, statements, hits, err := parseBlock(line)
		if err != nil {
			return nil, err
		}
		if !gated(pkg) {
			continue
		}
		s := stats[pkg]
		s.statements += statements
		if hits > 0 {
			s.covered += statements
		}
		stats[pkg] = s
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	return stats, nil
}

func parseBlock(line string) (pkg string, statements, hits int, err error) {
	file, rest, ok := strings.Cut(line, ":")
	fields := strings.Fields(rest)
	if !ok || len(fields) != profileFields {
		return "", 0, 0, fmt.Errorf("%w: %q", ErrProfileSyntax, line)
	}
	statements, err = strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: %q", ErrProfileSyntax, line)
	}
	hits, err = strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: %q", ErrProfileSyntax, line)
	}
	return path.Dir(strings.TrimPrefix(file, modulePrefix)), statements, hits, nil
}

// gated reports whether a package directory is subject to the coverage floor:
// internal packages except generated code; cmd and tools are excluded.
func gated(pkg string) bool {
	return strings.HasPrefix(pkg, "internal/") && !strings.HasPrefix(pkg, generatedTree)
}

func floor(pkg string) float64 {
	switch pkg {
	case "internal/kernel", "internal/codec", "internal/execstore",
		"internal/coordinator", "internal/crypto", "internal/vault":
		return strictFloor
	default:
		return defaultFloor
	}
}

func evaluate(stats map[string]packageStats, baseline map[string]float64) []string {
	var problems []string
	for _, pkg := range slices.Sorted(maps.Keys(stats)) {
		s := stats[pkg]
		if s.statements == 0 {
			continue
		}
		pct := s.percent()
		if pct < floor(pkg) {
			problems = append(problems, fmt.Sprintf("%s: %.1f%% is below the %.0f%% floor", pkg, pct, floor(pkg)))
		}
		if base, ok := baseline[pkg]; ok && pct < base-baselineSlack {
			problems = append(problems, fmt.Sprintf("%s: %.1f%% dropped more than %.1f points below the %.1f%% baseline", pkg, pct, baselineSlack, base))
		}
	}
	return problems
}

func raised(stats map[string]packageStats, baseline map[string]float64) map[string]float64 {
	out := maps.Clone(baseline)
	if out == nil {
		out = map[string]float64{}
	}
	for pkg, s := range stats {
		if s.statements == 0 {
			continue
		}
		out[pkg] = math.Max(out[pkg], math.Floor(s.percent()*10)/10)
	}
	return out
}

func readBaseline(name string) (map[string]float64, error) {
	data, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	var baseline map[string]float64
	if err := json.Unmarshal(data, &baseline); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBaselineInvalid, err)
	}
	return baseline, nil
}

func writeBaseline(name string, baseline map[string]float64) error {
	data, err := json.MarshalIndent(baseline, "", baselineIndent)
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}
	if err := os.WriteFile(name, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	return nil
}
