package transcriptview

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/session"
)

// budgetItems builds the 2,000 mixed items the budget measures: messages with
// markdown, tool calls with output, file changes with diffs, and the compact
// kinds, in a repeating mix.
func budgetItems(n int) []*session.Item {
	items := make([]*session.Item, n)
	for i := range items {
		id := fmt.Sprintf("b%d", i)
		switch i % 8 {
		case 0, 1, 2:
			items[i] = message(id, uint64(i+1), sampleParagraph(i))
		case 3:
			items[i] = &session.Item{TranscriptItem: client.TranscriptItem{
				ItemID: id, Kind: "tool_call", Sequence: uint64(i + 1), Revision: 1,
				Metadata: client.TranscriptMetadata{Tool: "read_file", Status: "succeeded", ExitCode: int32Ptr(0), DurationMillis: 12},
				View:     client.TranscriptView{Text: sampleOutput(i)},
			}}
		case 4:
			items[i] = message(id, uint64(i+1), sampleCode(i))
		case 5:
			items[i] = &session.Item{TranscriptItem: client.TranscriptItem{
				ItemID: id, Kind: "file_change", Sequence: uint64(i + 1), Revision: 1,
				Metadata: client.TranscriptMetadata{ChangeKind: "modify", LinesAdded: 21, LinesRemoved: 4},
				View:     client.TranscriptView{Text: sampleDiff(i)},
			}}
		case 6:
			items[i] = message(id, uint64(i+1), sampleParagraph(i))
		default:
			items[i] = &session.Item{TranscriptItem: client.TranscriptItem{
				ItemID: id, Kind: "usage", Sequence: uint64(i + 1), Revision: 1,
				Metadata: client.TranscriptMetadata{InputTokens: 1200, OutputTokens: 300, CacheTokens: 40},
			}}
		}
	}
	return items
}

// budgetHarness is the scripted pane: scrolling and streaming advance on the
// frames the benchmark drives.
type budgetHarness struct {
	h    *harness
	step int
}

// newBudgetHarness builds the 2,000-item pane and lays the first frame so the
// measurements start warm the way a live transcript is.
func newBudgetHarness(t *testing.T) *budgetHarness {
	t.Helper()
	h := newHarness(t, 900, 800, budgetItems(2000))
	h.frame()
	return &budgetHarness{h: h}
}

// frame advances the script one frame: scroll by a card, and every few frames
// stream into an open item — a new revision the cache must re-lay out.
func (bh *budgetHarness) frame() layout.Dimensions {
	bh.step++
	bh.h.scroll(estimateH)
	open := bh.h.items[bh.step%len(bh.h.items)]
	open.Revision++
	open.TranscriptItem.View.Text = sampleParagraph(bh.step)
	return bh.h.frame()
}

// measureBudget drives the scripted scroll and stream and times the frames.
func measureBudget(t *testing.T) (warmP95 time.Duration, frames int) {
	t.Helper()
	bh := newBudgetHarness(t)
	warmP95 = time.Hour
	for range 4 {
		var samples []time.Duration
		testing.Benchmark(func(b *testing.B) {
			b.Helper()
			for b.Loop() {
				start := b.Elapsed()
				bh.frame()
				samples = append(samples, b.Elapsed()-start)
			}
		})
		slices.Sort(samples)
		warmP95 = min(warmP95, samples[len(samples)*95/100])
		frames += len(samples)
	}
	return warmP95, frames
}

// logBudget prints the measurements.
func logBudget(t *testing.T, warmP95 time.Duration, frames int) {
	t.Helper()
	t.Logf("2,000 mixed items, scripted scroll and stream: layout p95 %v over %d frames", warmP95, frames)
}

// sampleParagraph, sampleCode, sampleOutput and sampleDiff build the mixed
// content of the budget items.
func sampleParagraph(i int) string {
	return fmt.Sprintf("## Turn %d\n\nA paragraph with **bold**, `inline code` and a [link](https://example.com/%d).\n\n- one\n- two\n", i, i)
}

func sampleCode(i int) string {
	return fmt.Sprintf("```go\nfunc f%d() int {\n\treturn %d\n}\n```\n", i, i)
}

func sampleOutput(i int) string {
	return fmt.Sprintf("line one of the output %d\nline two\nline three\nerror: none\n", i)
}

func sampleDiff(i int) string {
	return fmt.Sprintf("--- a/file%d.go\n+++ b/file%d.go\n@@ -1,3 +1,4 @@\n one\n-two\n+two changed\n+added\n", i, i)
}
