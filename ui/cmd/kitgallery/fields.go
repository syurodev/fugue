package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The TextField and TextArea pages (UK-09, ui-kit §6.8–6.9): every variant
// × state in the window's mode, laid out from measured sizes in the fixed
// window and recorded for the evidence tests like the button pages. The
// §4.1 interaction states render through the gallery's forced preview;
// filled, error and disabled come from the props, and the secret page
// shows the masked and the revealed field side by side.

// The field state columns, one per §4.1 state the fields show. The secret
// variant renames rest to masked and fills the filled column with the
// revealed field (§6.8).
const (
	fstRest     = "rest"
	fstHover    = "hover"
	fstFocus    = "focus"
	fstFilled   = "filled"
	fstError    = "error"
	fstDisabled = "disabled"
	fstMasked   = "masked"
	fstRevealed = "revealed"
)

// The fixed tables of the field matrices.
const (
	numFieldVariants = 4 // text, search, secret, number
	numFieldStates   = 6
	numAreaStates    = 6
)

// vietnamese is the §3.3 stress string the filled text field carries.
const vietnamese = "Ễ Ặ Ỡ Ậ ỷ ộ"

// fieldStates are the columns of the text, search and number blocks, and
// of the TextArea page.
func fieldStates() []string {
	return []string{fstRest, fstHover, fstFocus, fstFilled, fstError, fstDisabled}
}

// secretStates are the columns of the secret block: the masked field at
// rest, the interaction states, the revealed field in place of filled, and
// the masked field in error.
func secretStates() []string {
	return []string{fstMasked, fstHover, fstFocus, fstRevealed, fstError, fstDisabled}
}

// textVariantNames label the field blocks, in §6.8 order.
func textVariantNames() []string {
	return []string{"text", "search", "secret", "number (ms, 5–100)"}
}

// newFieldStates allocates the field matrices.
func (p *pages) newFieldStates() {
	for v := range p.text {
		for c := range p.text[v] {
			p.text[v][c] = kit.NewTextFieldState()
		}
	}
	for c := range p.area {
		p.area[c] = kit.NewTextAreaState()
	}
	p.prepareFields()
}

// prepareFields fills the per-cell state once: the filled cells carry
// their text, the error cells are marked submitted so the §6.8 gate stands
// open, and the revealed cell is revealed.
func (p *pages) prepareFields() {
	for ci, st := range fieldStates() {
		p.text[0][ci].SetText(fieldText(st, "host.one", vietnamese))
		p.text[1][ci].SetText(fieldText(st, "", "viết"))
		p.text[3][ci].SetText(fieldText(st, "", "42"))
		if st == fstError {
			for _, v := range []int{0, 1, 3} {
				p.text[v][ci].MarkSubmitted()
			}
			p.text[3][ci].SetText("200")
		}
	}
	// Secret: masked at rest and under interaction, revealed in the filled
	// column, masked in error.
	for ci, st := range secretStates() {
		p.text[2][ci].SetText("s3cret-value")
		p.text[2][ci].SetRevealed(st == fstRevealed)
		if st == fstError {
			p.text[2][ci].MarkSubmitted()
		}
	}
	for ci, st := range fieldStates() {
		p.area[ci].SetText(fieldText(st, "", "line one\nline two\nline three\nline four"))
		if st == fstError {
			p.area[ci].MarkSubmitted()
		}
	}
}

// fieldText is the value of one filled cell: rest, hover and focus stay
// empty behind their placeholder, the other columns carry content.
func fieldText(st, empty, filled string) string {
	switch st {
	case fstRest, fstHover, fstFocus:
		return empty
	default:
		return filled
	}
}

// layoutTextFields draws the TextField page: two blocks per variant, three
// states each, so a block and its own captions never grow wider than the
// page column at any zoom (UK-E09).
func (p *pages) layoutTextFields(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "TextField §6.8: a fixed label above — the placeholder is never the label — the flat FillSoft box, helper or error below. The search field clears on Escape; the secret field refuses copy and cut while masked; an empty number field is never 0.") + gtx.Dp(theme.SpaceXL)
	blocks := make([]block, 0, 2*numFieldVariants)
	for vi, name := range textVariantNames() {
		states := fieldStates()
		if vi == 2 {
			states = secretStates()
		}
		for half := range 2 {
			cols := states[half*3 : half*3+3]
			blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
				return p.matrix(gtx, name, cols, p.textRows(gtx, vi, half), dry)
			})
		}
	}
	p.flow(gtx, y, blocks)
}

// layoutTextArea draws the TextArea page.
func (p *pages) layoutTextArea(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "TextArea §6.9: the same core, flowing. Enter inserts a newline; the submit key belongs to the caller. The box grows with its content to three lines here, then the rest scrolls inside.") + gtx.Dp(theme.SpaceXL)
	blocks := make([]block, 0, 2)
	for half := range 2 {
		cols := fieldStates()[half*3 : half*3+3]
		blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "message", cols, p.areaRows(gtx, half), dry)
		})
	}
	p.flow(gtx, y, blocks)
}

// textRows is the one row of a field block: three states of the variant.
func (p *pages) textRows(gtx *layout.Context, vi, half int) [][]cellDraw {
	states := fieldStates()
	if vi == 2 {
		states = secretStates()
	}
	row := make([]cellDraw, 0, 3)
	for ci, st := range states[half*3 : half*3+3] {
		col := half*3 + ci
		pr := fieldCellProps(vi, st)
		s := p.text[vi][col]
		row = append(row, cellDraw{
			col: ci, key: cell{compTextField, vi, 0, st}, tag: s.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return s.Measure(p.env, gtx, &pr).Size }),
			lay: func(gtx *layout.Context) {
				if _, err := s.Layout(p.env, gtx, &pr); err != nil {
					p.err = err
				}
			},
		})
	}
	return [][]cellDraw{row}
}

// areaRows is the one row of a TextArea block.
func (p *pages) areaRows(gtx *layout.Context, half int) [][]cellDraw {
	row := make([]cellDraw, 0, 3)
	for ci, st := range fieldStates()[half*3 : half*3+3] {
		col := half*3 + ci
		pr := areaCellProps(st)
		s := p.area[col]
		row = append(row, cellDraw{
			col: ci, key: cell{compTextArea, 0, 0, st}, tag: s.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return s.Measure(p.env, gtx, &pr).Size }),
			lay: func(gtx *layout.Context) {
				// The area reports no error; the events are the caller's
				// business and the matrix reads none.
				_ = s.Layout(p.env, gtx, &pr)
			},
		})
	}
	return [][]cellDraw{row}
}

// fieldCellProps are the props of one TextField matrix cell: the state
// columns come from the props (filled, error, disabled) or from the
// preview (hover, focus), exactly as §4.1 stacks them. Every cell names a
// label — the identifier — and the empty ones a placeholder, which is
// never the label (§6.8).
func fieldCellProps(vi int, st string) kit.TextFieldProps {
	pr := kit.TextFieldProps{
		Label:       "Label",
		Placeholder: "placeholder",
		Helper:      "helper text",
		Variant:     kit.TextFieldVariant(vi),
	}
	if st == fstError {
		pr.Error = "an error message"
	}
	if st == fstDisabled {
		pr.Disabled = true
	}
	if pr.Variant == kit.TextFieldNumber {
		pr.Unit = "ms"
		lo, hi := 5.0, 100.0
		pr.Min, pr.Max = &lo, &hi
	}
	return pr
}

// areaCellProps are the props of one TextArea matrix cell.
func areaCellProps(st string) kit.TextAreaProps {
	pr := kit.TextAreaProps{
		Label:       "Label",
		Placeholder: "placeholder",
		Helper:      "helper text",
		MaxLines:    3,
	}
	if st == fstError {
		pr.Error = "an error message"
	}
	if st == fstDisabled {
		pr.Disabled = true
	}
	return pr
}
