package diff

import (
	"strings"
	"testing"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
)

// The highlighted items must carry exactly the text of the unified diff, so a
// selection copies the diff itself.
func TestItemsCarryTheDiffText(t *testing.T) {
	a := Source()
	lines, b := Make(a, 5000, 1)
	es, _, err := Entries(lines, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) < 5000 {
		t.Fatalf("only %d diff lines", len(es))
	}
	items := make([]flow.Item, len(es))
	for i, e := range es {
		items[i] = flow.Item{Blocks: e.Body}
		want := lines[i].Text
		if lines[i].Kind != '@' {
			want = string(lines[i].Kind) + want
		}
		if got := items[i].Text(); got != want {
			t.Fatalf("line %d: item text %q, diff line %q", i, got, want)
		}
	}
	sel := flow.Selector{Sep: "\n", Text: func(i int) string { return items[i].Text() }, Has: true,
		Anchor: flow.Pos{Item: 100, Off: 0}, Focus: flow.Pos{Item: 109, Off: len([]rune(items[109].Text()))}}
	var want []string
	for i := 100; i <= 109; i++ {
		want = append(want, items[i].Text())
	}
	if got := sel.SelectedText(); got != strings.Join(want, "\n") {
		t.Fatalf("copy of lines 100..109 differs:\n%q\n%q", got, strings.Join(want, "\n"))
	}
}
