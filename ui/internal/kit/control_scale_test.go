package kit

import (
	"image"
	"image/color"
	"strconv"
	"testing"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestUKE14ControlHeightsAtEverySize covers UK-E14: Button and IconButton
// measure 24/28/32/36 dp at xs/sm/md/lg, and TextField, Select, Combobox and
// SegmentedControl measure 32 dp (ControlMD) with uniform form metrics.
func TestUKE14ControlHeightsAtEverySize(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.frame(t)
	checkButtonAndIconHeights(t, h)
	checkFormControlHeights(t, h)
	checkButtonMetrics(t)
	h.end()
}

func checkButtonAndIconHeights(t *testing.T, h *harness) {
	t.Helper()
	btn := NewButtonState()
	ibtn := NewIconButtonState()
	sizes := []struct {
		name string
		size ButtonSize
		want int
	}{
		{"xs", SizeXS, h.gtx.Dp(theme.ControlXS)},
		{"sm", SizeSM, h.gtx.Dp(theme.ControlSM)},
		{"md", SizeMD, h.gtx.Dp(theme.ControlMD)},
		{"lg", SizeLG, h.gtx.Dp(theme.ControlLG)},
	}
	for _, tc := range sizes {
		gotBtn := btn.Measure(h.env, &h.gtx, ButtonProps{Text: "Build", Icon: IconRestart, Size: tc.size}).Size.Y
		if gotBtn != tc.want {
			t.Errorf("Button(%s) height = %d, want %d", tc.name, gotBtn, tc.want)
		}
		gotIcon := ibtn.Measure(h.env, &h.gtx, &IconButtonProps{Icon: IconClose, Label: "Close", Size: tc.size}).Size
		if gotIcon != image.Pt(tc.want, tc.want) {
			t.Errorf("IconButton(%s) size = %v, want %dx%d", tc.name, gotIcon, tc.want, tc.want)
		}
	}
}

func checkFormControlHeights(t *testing.T, h *harness) {
	t.Helper()
	wantMD := h.gtx.Dp(theme.ControlMD)
	wantFieldNoLabel := wantMD + h.gtx.Dp(theme.SpaceXS)
	if fm := fieldMetrics(h.env, &h.gtx, ""); fm.boxH != wantMD {
		t.Errorf("fieldMetrics boxH = %d, want %d", fm.boxH, wantMD)
	}
	tf := NewTextFieldState()
	if got := tf.Measure(h.env, &h.gtx, &TextFieldProps{Placeholder: "Name"}).Size.Y; got != wantFieldNoLabel {
		t.Errorf("TextField height = %d, want %d", got, wantFieldNoLabel)
	}
	sel := NewSelectState()
	if got := sel.Measure(h.env, &h.gtx, &SelectProps{Placeholder: "Pick preset", Options: []SelectOption{{ID: "a", Label: "Beta"}}}).Size.Y; got != wantFieldNoLabel {
		t.Errorf("Select height = %d, want %d", got, wantFieldNoLabel)
	}
	cb := NewComboboxState()
	if got := cb.Measure(h.env, &h.gtx, &ComboboxProps{Placeholder: "Workspace"}).Size.Y; got != wantFieldNoLabel {
		t.Errorf("Combobox height = %d, want %d", got, wantFieldNoLabel)
	}
	seg := NewSegmentedState()
	if got := seg.Measure(h.env, &h.gtx, SegmentedProps{Options: []SegmentedOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}).Size.Y; got != wantMD {
		t.Errorf("SegmentedControl height = %d, want %d", got, wantMD)
	}
}

func checkButtonMetrics(t *testing.T) {
	t.Helper()
	xsPad, xsGap := buttonMetrics(SizeXS)
	if xsPad != theme.SpaceMD || xsGap != theme.SpaceXS || buttonIconSize(SizeXS) != theme.IconSM {
		t.Errorf("xs metrics = (%v, %v, %v), want (%v, %v, %v)", xsPad, xsGap, buttonIconSize(SizeXS), theme.SpaceMD, theme.SpaceXS, theme.IconSM)
	}
	for _, s := range []ButtonSize{SizeSM, SizeMD, SizeLG} {
		pad, gap := buttonMetrics(s)
		if pad != theme.SpaceLG || gap != theme.SpaceSM || buttonIconSize(s) != theme.IconMD {
			t.Errorf("size %d metrics = (%v, %v, %v), want (%v, %v, %v)", s, pad, gap, buttonIconSize(s), theme.SpaceLG, theme.SpaceSM, theme.IconMD)
		}
	}
}

type formDialogProbe struct {
	d                  *DialogState
	cancel, pri, extra *ButtonState
	tf                 *TextFieldState
	sel                *SelectState
	cb                 *ComboboxState
	selProps           *SelectProps
	tfProps            *TextFieldProps
	cbProps            *ComboboxProps
	colW, tfW, selW    int
	cbW                int
	selBox             image.Rectangle
	bodyOrigin         image.Point
}

func newFormDialogProbe() *formDialogProbe {
	return &formDialogProbe{
		d:      NewDialogState(),
		cancel: NewButtonState(), pri: NewButtonState(), extra: NewButtonState(),
		tf:  NewTextFieldState(),
		sel: NewSelectState(),
		cb:  NewComboboxState(),
		selProps: &SelectProps{
			Label:       "Target",
			Placeholder: "Pick an option",
			Options:     []SelectOption{{ID: "a", Label: "Short"}, {ID: "b", Label: "A much longer engine label"}},
			Block:       true,
		},
		tfProps: &TextFieldProps{Label: "Name", Placeholder: "session"},
		cbProps: &ComboboxProps{Label: "Workspace", Placeholder: "Pick workspace"},
	}
}

func (p *formDialogProbe) layoutBody(t *testing.T, env *Env, gtx *layout.Context, bands *DialogBands) image.Point {
	t.Helper()
	p.colW = gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceLG)
	y := 0

	tfDims := p.tf.Measure(env, gtx, p.tfProps)
	p.tfW = tfDims.Size.X
	if _, err := p.tf.Layout(env, gtx, p.tfProps); err != nil {
		t.Fatalf("textfield layout: %v", err)
	}
	bands.Focusable(p.tf.Tag(), y, y+tfDims.Size.Y)
	y += tfDims.Size.Y + gap

	selDims := p.sel.Measure(env, gtx, p.selProps)
	p.selW = selDims.Size.X
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) {
		if _, err := p.sel.Layout(env, gtx, p.selProps); err != nil {
			t.Fatalf("select layout: %v", err)
		}
	})
	sz := fieldMetrics(env, gtx, p.selProps.Label)
	p.selBox = image.Rect(0, y+sz.labelH, p.selW, y+sz.labelH+sz.boxH)
	bands.Focusable(p.sel.Tag(), y, y+selDims.Size.Y)
	y += selDims.Size.Y + gap

	cbDims := p.cb.Measure(env, gtx, p.cbProps)
	p.cbW = cbDims.Size.X
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) {
		if _, err := p.cb.Layout(env, gtx, p.cbProps); err != nil {
			t.Fatalf("combobox layout: %v", err)
		}
	})
	bands.Focusable(p.cb.Tag(), y, y+cbDims.Size.Y)
	return image.Pt(p.colW, y+cbDims.Size.Y)
}

// TestUKE14DialogFormColumnSpanSelectPlaceholderAndFooter covers UK-E14 §4.6:
// inside a DialogForm, TextField, Select (Block) and Combobox span the column,
// an unselected Select keeps full width and renders Placeholder text (while a
// non-Block Select keeps its longest-option width), and footer buttons match
// the input height (ControlMD = 32 dp).
func TestUKE14DialogFormColumnSpanSelectPlaceholderAndFooter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	p := newFormDialogProbe()
	props := &DialogProps{
		Variant: DialogForm, Title: "Edit item",
		Cancel: p.cancel, Primary: p.pri, PrimaryLabel: "Create",
		Extra: p.extra, ExtraLabel: "Reset",
		Body: func(gtx *layout.Context, bands *DialogBands) image.Point { return p.layoutBody(t, h.env, gtx, bands) },
	}

	p.d.Open()
	var res DialogResult
	for range 4 {
		runFrame(t, h, func(gtx *layout.Context) {
			paint.FillShape(gtx.Ops, h.env.Th.Chrome().AppBg, clip.Rect(h.env.windowRect()).Op())
			var err error
			if res, err = p.d.Layout(h.env, gtx, props); err != nil {
				t.Fatalf("dialog layout: %v", err)
			}
			p.bodyOrigin = res.Boxes.Body.Min.Add(image.Pt(dialogRing(gtx), 0))
		})
	}
	img := h.shot(t, h.env.win.X, h.env.win.Y)
	assertFormColumnAndFooter(t, h, p, &res)
	assertUnselectedSelectPlaceholder(t, h, p, img)
}

func assertFormColumnAndFooter(t *testing.T, h *harness, p *formDialogProbe, res *DialogResult) {
	t.Helper()
	if p.tfW != p.colW || p.selW != p.colW || p.cbW != p.colW {
		t.Fatalf("form controls did not span column %d: tf=%d sel=%d cb=%d", p.colW, p.tfW, p.selW, p.cbW)
	}
	wantH := h.gtx.Dp(theme.ControlMD)
	for _, tc := range []struct {
		name string
		box  image.Rectangle
	}{{"Cancel", res.Boxes.Cancel}, {"Primary", res.Boxes.Primary}, {"Extra", res.Boxes.Extra}} {
		if tc.box.Dy() != wantH {
			t.Errorf("dialog footer %s height = %d, want %d", tc.name, tc.box.Dy(), wantH)
		}
	}
}

func assertUnselectedSelectPlaceholder(t *testing.T, h *harness, p *formDialogProbe, img *image.RGBA) {
	t.Helper()
	inlineGtx := h.gtx
	inlineGtx.Constraints = layout.Constraints{Max: image.Pt(p.colW, 200)}
	nonBlockProps := *p.selProps
	nonBlockProps.Block = false
	if nonBlockW := p.sel.Measure(h.env, &inlineGtx, &nonBlockProps).Size.X; nonBlockW >= p.colW {
		t.Errorf("non-Block Select width = %d, want < column width %d", nonBlockW, p.colW)
	}
	screenSel := p.selBox.Add(p.bodyOrigin)
	textArea := image.Rect(
		screenSel.Min.X+h.gtx.Dp(theme.SpaceLG), screenSel.Min.Y+4,
		screenSel.Max.X-h.gtx.Dp(theme.SpaceLG)-h.gtx.Dp(theme.IconMD)-h.gtx.Dp(theme.SpaceSM), screenSel.Max.Y-4,
	)
	bg := pixel(img, screenSel.Min.X+2, screenSel.Min.Y+2)
	_, _, _, inkCount := scan(img, textArea, func(c color.RGBA) bool { return colorDist(c, bg) > 25 })
	if inkCount == 0 {
		t.Fatalf("unselected Select drew an empty box in %v (expected Placeholder ink)", textArea)
	}
}

// TestUKE14XSControlsFitRowTabAndHeaderWithUnclippedFocusRing covers UK-E14:
// xs controls (24 dp) inside a 28 dp Row, a 32 dp Tab strip, and a 30 dp header
// fit with at least 2 dp vertical margin and render an unclipped focus ring.
func TestUKE14XSControlsFitRowTabAndHeaderWithUnclippedFocusRing(t *testing.T) {
	t.Parallel()
	t.Run("listrow 28dp", checkListRowXSFocusRing)
	t.Run("tabs 32dp", checkTabsXSFocusRing)
	t.Run("header 30dp", checkHeaderXSFocusRing)
}

func checkListRowXSFocusRing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	row := NewListRowState()
	row.ensureActions(1)
	pr := &ListRowProps{Label: "Session", Actions: []ListAction{{ID: "pin", Icon: IconPin, Label: "Pin"}}}
	origin := image.Pt(40, 40)
	drawRow := func(pv Preview) *image.RGBA {
		h.env.Preview = pv
		h.frameSize(t, 360, 120)
		paint.Fill(h.gtx.Ops, h.env.Th.Chrome().CanvasBg)
		at(h.gtxPtr(), origin, func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Constraints{Max: image.Pt(280, 100)}
			row.Layout(h.env, &g, pr)
		})
		img := h.shot(t, 360, 120)
		h.end()
		return img
	}
	on := drawRow(Preview{Hover: row.Tag(), Focus: row.actions[0].Tag()})
	off := drawRow(Preview{Hover: row.Tag()})

	btnH, rowH := h.gtx.Dp(theme.ControlXS), h.gtx.Dp(theme.Row)
	if margin := (rowH - btnH) / 2; margin < 2 {
		t.Fatalf("row action vertical margin = %d, want >= 2", margin)
	}
	inset := h.gtx.Dp(theme.SpaceXS)
	btnX := origin.X + inset + (280 - 2*inset) - h.gtx.Dp(theme.SpaceMD) - btnH
	btnBox := image.Rect(btnX, origin.Y+(rowH-btnH)/2, btnX+btnH, origin.Y+(rowH-btnH)/2+btnH)
	assertFocusRingEdges(t, &h.gtx, btnBox, on, off)
}

func checkTabsXSFocusRing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	tabs := NewTabsState()
	pr := TabsProps{
		Variant: TabsDocument, ActiveID: "t1",
		Items: []TabItem{{ID: "t1", Label: "First document tab"}, {ID: "t2", Label: "Second document tab"}, {ID: "t3", Label: "Third document tab"}},
	}
	origin := image.Pt(20, 40)
	var dims layout.Dimensions
	drawTabs := func(pv Preview) *image.RGBA {
		h.env.Preview = pv
		h.frameSize(t, 340, 120)
		paint.Fill(h.gtx.Ops, h.env.Th.Chrome().CanvasBg)
		at(h.gtxPtr(), origin, func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Constraints{Max: image.Pt(240, 100)}
			dims, _ = tabs.Layout(h.env, &g, pr)
		})
		img := h.shot(t, 340, 120)
		h.end()
		return img
	}
	on := drawTabs(Preview{Focus: tabs.overflowBtn.Tag()})
	off := drawTabs(Preview{})
	if len(tabs.HiddenIDs()) == 0 {
		t.Fatal("expected overflowing tabs so overflow button lays out")
	}
	ovPr := ButtonProps{Text: strconv.Itoa(len(tabs.HiddenIDs())), Icon: IconChevronDown, Variant: Ghost, Size: SizeXS}
	ovSz := tabs.overflowBtn.Measure(h.env, &h.gtx, ovPr).Size
	if ovSz.Y != h.gtx.Dp(theme.ControlXS) || (dims.Size.Y-ovSz.Y)/2 < 2 {
		t.Fatalf("overflow height=%d in strip=%d, want %d with >=2 margin", ovSz.Y, dims.Size.Y, h.gtx.Dp(theme.ControlXS))
	}
	bx, by := origin.X+tabs.StripRect().Max.X+h.gtx.Dp(theme.SpaceXS), origin.Y+(dims.Size.Y-ovSz.Y)/2
	assertFocusRingEdges(t, &h.gtx, image.Rect(bx, by, bx+ovSz.X, by+ovSz.Y), on, off)
}

func checkHeaderXSFocusRing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	btn := NewIconButtonState()
	pr := &IconButtonProps{Icon: IconClose, Label: "Close slot", Variant: IconButtonGhost, Size: SizeXS}
	headerH := h.gtx.Dp(theme.SlotHeaderHeight)
	btnSide := btn.Measure(h.env, &h.gtx, pr).Size.X
	margin := (headerH - btnSide) / 2
	if margin < 2 {
		t.Fatalf("30 dp header margin = %d, want >= 2", margin)
	}
	btnPos := image.Pt(160-h.gtx.Dp(theme.SpaceXS)-btnSide, 40+margin)
	drawBtn := func(pv Preview) *image.RGBA {
		h.env.Preview = pv
		h.frameSize(t, 240, 120)
		paint.Fill(h.gtx.Ops, h.env.Th.Chrome().CanvasBg)
		at(h.gtxPtr(), btnPos, func(gtx *layout.Context) {
			if _, err := btn.Layout(h.env, gtx, pr); err != nil {
				t.Fatalf("icon button layout: %v", err)
			}
		})
		img := h.shot(t, 240, 120)
		h.end()
		return img
	}
	on := drawBtn(Preview{Focus: btn.Tag()})
	off := drawBtn(Preview{})
	assertFocusRingEdges(t, &h.gtx, image.Rectangle{Min: btnPos, Max: btnPos.Add(image.Pt(btnSide, btnSide))}, on, off)
}

func (h *harness) gtxPtr() *layout.Context { return &h.gtx }

func assertFocusRingEdges(t *testing.T, gtx *layout.Context, box image.Rectangle, on, off *image.RGBA) {
	t.Helper()
	ring := gtx.Dp(theme.FocusOffset) + gtx.Dp(theme.FocusStroke)/2
	samples := []struct {
		name string
		pt   image.Point
	}{
		{"top", image.Pt((box.Min.X+box.Max.X)/2, box.Min.Y-ring)},
		{"bottom", image.Pt((box.Min.X+box.Max.X)/2, box.Max.Y+ring)},
		{"left", image.Pt(box.Min.X-ring, (box.Min.Y+box.Max.Y)/2)},
		{"right", image.Pt(box.Max.X+ring, (box.Min.Y+box.Max.Y)/2)},
	}
	for _, s := range samples {
		a, b := on.RGBAAt(s.pt.X, s.pt.Y), off.RGBAAt(s.pt.X, s.pt.Y)
		if colorDist(a, b) < 30 {
			t.Errorf("focus ring %s edge at %v clipped or missing: on=%v off=%v", s.name, s.pt, a, b)
		}
	}
}
