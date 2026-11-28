package kit

import (
	"testing"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestSectionHeaderBadgeRendersWithoutError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewSectionHeaderState(true)

	h.frameSize(t, 260, 100)
	dims, ev := st.Layout(h.env, &h.gtx, &SectionHeaderProps{
		Label:       "Workspace Alpha",
		Icon:        IconFolder,
		Inset:       theme.SpaceLG,
		Collapsible: true,
		Badge:       "3",
		Action:      &IconButtonProps{Icon: IconEllipsis, Label: "Manage workspace"},
	}, nil)
	if ev.Err != nil {
		t.Fatalf("SectionHeader with badge error: %v", ev.Err)
	}
	if dims.Size.X != 260 || dims.Size.Y <= 0 {
		t.Fatalf("unexpected dims: %+v", dims)
	}
}
