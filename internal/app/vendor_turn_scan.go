package app

// End-of-turn secret scan (git-guard §3.3, GG-04). When a turn ends, the lines
// the turn added are read from the two shadow trees and run through the shared
// detector. A hit adds one notice(possible_secret); it never changes the
// session state and never asks the user.
//
// Where the bytes go: added lines are streamed from git into one bounded,
// reused buffer that is cleared when the scan ends. Each line is matched, and
// the only value-derived data kept is the masked form of a hit (at most four
// leading and two trailing characters, and only for values of twelve or more
// characters). The masked form is written to the notice's sealed content. The
// metadata holds counts and a flag only. The real value is never stored,
// logged or placed in an error.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/snapshot"
	"github.com/syurodev/fugue/internal/transcript"
)

const (
	// TurnScanCap bounds the added content scanned for one turn (git-guard §6).
	// Past it the head is scanned and the notice says the scan is incomplete.
	TurnScanCap int64 = 20 << 20
	// maxListedFindings bounds the sealed list of one notice; the metadata
	// count still covers every hit.
	maxListedFindings = 200
	// maskHead and maskTail are the characters of a value a masked hit shows;
	// a value shorter than minMaskedLen shows its rule only (content-safety §3.8).
	maskHead     = 4
	maskTail     = 2
	minMaskedLen = 12
)

// TurnAddedLines is the optional port, next to TurnSnapshotter, that reads the
// lines a turn added from its snapshot pair. A snapshotter without it gets no
// end-of-turn secret scan.
type TurnAddedLines interface {
	AddedLines(ctx context.Context, snap TurnSnapshot, changes *TurnChanges, maxBytes int64, visit func(*snapshot.AddedLine)) (snapshot.AddedScan, error)
}

// AddedLines streams the lines added between the turn's two shadow trees.
func (e *engineTurnSnapshots) AddedLines(ctx context.Context, snap TurnSnapshot, changes *TurnChanges, maxBytes int64, visit func(*snapshot.AddedLine)) (snapshot.AddedScan, error) {
	repo, err := e.engine.Open(ctx, snap.WorkDir)
	if err != nil {
		return snapshot.AddedScan{}, fmt.Errorf("open the shadow repository: %w", err)
	}
	scan, err := repo.AddedLines(ctx, changes.BeforeTree, changes.AfterTree, maxBytes, visit)
	if err != nil {
		return scan, fmt.Errorf("read the turn's added lines: %w", err)
	}
	return scan, nil
}

// scanFinding is one hit as the notice shows it. Mask is the masked value, never
// the value.
type scanFinding struct {
	Kind, Path, Mask string
	Line             int
}

// turnScanResult is the outcome of one scan, safe to keep: counts, rule kinds,
// paths and masked values only.
type turnScanResult struct {
	Findings   []scanFinding
	Total      int
	Files      int
	Binary     int
	Incomplete bool
}

// turnScanCollector gathers hits while the scan runs on its own goroutine. When
// the scan outlives its cap the actor takes what was found so far and closes the
// collector, so a late line from the stopped goroutine changes nothing.
type turnScanCollector struct {
	mu       sync.Mutex
	closed   bool
	detector *secrets.Detector
	result   turnScanResult
	files    map[string]struct{}
}

func newTurnScanCollector() *turnScanCollector {
	return &turnScanCollector{detector: secrets.New(nil, secrets.Options{}), files: make(map[string]struct{})}
}

// visit matches one added line. The line text aliases a buffer the scan clears
// afterwards; the string copy made for the detector is a short-lived temporary.
func (c *turnScanCollector) visit(line *snapshot.AddedLine) {
	text := string(line.Text)
	found := c.detector.Find(text)
	if len(found) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	for i := range found {
		c.result.Total++
		c.files[line.Path] = struct{}{}
		if len(c.result.Findings) < maxListedFindings {
			c.result.Findings = append(c.result.Findings, scanFinding{
				Kind: found[i].Kind, Path: line.Path, Line: line.Number, Mask: maskValue(found[i].Kind, text[found[i].Start:found[i].End]),
			})
		}
	}
}

// finish closes the collector and returns what it holds.
func (c *turnScanCollector) finish(scan snapshot.AddedScan, incomplete bool) *turnScanResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	out := c.result
	out.Files = len(c.files)
	out.Binary = scan.Binary
	out.Incomplete = incomplete || scan.Truncated
	return &out
}

// maskValue shows at most the first four and last two characters of a value of
// twelve or more characters, and nothing of a shorter one or of a private key.
func maskValue(kind, value string) string {
	if kind == secrets.KindPrivateKey || utf8.RuneCountInString(value) < minMaskedLen {
		return ""
	}
	head, tail := 0, len(value)
	for range maskHead {
		_, size := utf8.DecodeRuneInString(value[head:])
		head += size
	}
	for range maskTail {
		_, size := utf8.DecodeLastRuneInString(value[:tail])
		tail -= size
	}
	return value[:head] + "…" + value[tail:]
}

// scanTurnAdditions scans the turn's added lines under their own cap and
// returns the hits, or nil when there is nothing to report. It never fails the
// turn: an error or a timeout keeps the hits found so far and marks the scan
// incomplete.
func (a *vendorSessionActor) scanTurnAdditions(ctx context.Context, start TurnSnapshot, changes *TurnChanges) *turnScanResult {
	reader, ok := a.manager.deps.TurnSnapshots.(TurnAddedLines)
	if !ok || changes.BeforeTree == "" || changes.AfterTree == "" || !addsLines(changes) {
		return nil
	}
	collector := newTurnScanCollector()
	limit := TurnScanCap
	if a.manager.deps.TurnScanBytes > 0 {
		limit = a.manager.deps.TurnScanBytes
	}
	scan, err := capped(ctx, a.manager, "turn-scan-"+a.info.ID, func(taskCtx context.Context) (snapshot.AddedScan, error) {
		return reader.AddedLines(taskCtx, start, changes, limit, collector.visit)
	})
	if err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the turn secret scan did not finish", "error_code", string(errcode.CodeOf(err)))
	}
	result := collector.finish(scan, err != nil)
	if result.Total == 0 {
		return nil
	}
	return result
}

// addsLines reports whether the diff has any added line to read.
func addsLines(changes *TurnChanges) bool {
	for i := range changes.Files {
		if changes.Files[i].Added != 0 {
			return true
		}
	}
	return false
}

// recordPossibleSecretNotice writes the notice for a scan with hits. The text is
// sealed content; the metadata carries counts and the incomplete flag only.
func (a *vendorSessionActor) recordPossibleSecretNotice(ctx context.Context, turnID string, result *turnScanResult) {
	if result == nil {
		return
	}
	id, err := a.manager.deps.IDs.NewID()
	if err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the secret notice could not get an identity")
		return
	}
	item := transcript.Item{
		ID: id, SessionID: a.info.ID, TurnID: turnID, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{
			Level: "warning", Code: transcript.NoticePossibleSecret,
			Findings: result.Total, Files: result.Files, ScanIncomplete: result.Incomplete,
		},
		Content: transcript.NoticeContent{Text: possibleSecretText(result)},
	}
	if err := a.recordItem(ctx, &item); err != nil {
		a.manager.deps.Logger.WarnContext(ctx, "the secret notice was not recorded", "error_code", string(errcode.CodeOf(err)))
	}
}

func possibleSecretText(result *turnScanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Possible secrets in lines added this turn: %d in %d files. Values are masked.\n", result.Total, result.Files)
	for i := range result.Findings {
		f := &result.Findings[i]
		fmt.Fprintf(&b, "- %s %s:%d", f.Kind, f.Path, f.Line)
		if f.Mask != "" {
			b.WriteString(" " + f.Mask)
		}
		b.WriteByte('\n')
	}
	if hidden := result.Total - len(result.Findings); hidden > 0 {
		fmt.Fprintf(&b, "%d more not listed.\n", hidden)
	}
	if result.Binary > 0 {
		fmt.Fprintf(&b, "%d binary files were not scanned.\n", result.Binary)
	}
	if result.Incomplete {
		b.WriteString("scan_incomplete: the scan stopped before covering every added line.\n")
	}
	return b.String()
}
