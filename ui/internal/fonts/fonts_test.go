package fonts

import (
	"strings"
	"testing"

	giofont "gioui.org/font"

	"go.uber.org/goleak"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// vietnameseSample exercises the full diacritic range of the Vietnamese
// alphabet (desktop-app DA-06): vowels with stacked and side diacritics, the
// horn vowels and the stroke letters, across all four tone-mark families.
const vietnameseSample = "Vụn ẮẶẰẲẴẾỀỂỄỆỈỊỌỘỐỒỔỖỚỜỞỠỨỪỬỮỰỲỴỶỸ Đđ ơư ạảấầẩẫậắằẳẵặẹẻẽếềểễệỉị" +
	"ọỏốồổỗộớờởỡợụủứừửữựỳỵỷỹ ĂăÂâÊêÔôƠơƯư"

// TestBundledFacesRenderTheVietnameseSample pins DA-04's coverage claim at the
// unit level: every rune of the Vietnamese sample has a real glyph in both
// bundled faces, so the sample renders with the bundled faces and the system
// fallback never has to answer for Vietnamese. A missing glyph maps to the
// notdef index 0.
func TestBundledFacesRenderTheVietnameseSample(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"data/Inter-Regular.ttf", "data/JetBrainsMono-Regular.ttf"} {
		f := parseFont(t, path)
		buf := &sfnt.Buffer{}
		for _, r := range vietnameseSample {
			if !hasGlyph(t, f, buf, path, r) {
				t.Errorf("%s has no glyph for %q", path, r)
			}
		}
	}
}

// parseFont parses one embedded font for the coverage checks.
func parseFont(t *testing.T, path string) *sfnt.Font {
	t.Helper()
	src, err := data.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	f, err := sfnt.Parse(src)
	if err != nil {
		t.Fatalf("%s: parse: %v", path, err)
	}
	return f
}

// hasGlyph reports whether the font has a real glyph for r.
func hasGlyph(t *testing.T, f *sfnt.Font, buf *sfnt.Buffer, path string, r rune) bool {
	t.Helper()
	if r == ' ' {
		return true
	}
	idx, err := f.GlyphIndex(buf, r)
	if err != nil {
		t.Fatalf("%s: glyph index of %q: %v", path, r, err)
	}
	return idx != 0
}

// TestBundledFacesParseAsACollection pins that the collection the shaper
// consumes parses and leads with the two bundled typefaces.
func TestBundledFacesParseAsACollection(t *testing.T) {
	t.Parallel()
	collection, err := Collection()
	if err != nil {
		t.Fatal(err)
	}
	if len(collection) < 2 {
		t.Fatalf("collection has %d faces, want at least the two bundled ones", len(collection))
	}
	got := make([]string, 0, len(collection))
	for _, face := range collection {
		got = append(got, string(face.Font.Typeface))
	}
	if !strings.Contains(strings.Join(got, ","), "Inter") ||
		!strings.Contains(strings.Join(got, ","), "JetBrains Mono") {
		t.Errorf("collection typefaces = %v, want Inter and JetBrains Mono", got)
	}
}

// TestShaperBuilds pins that the shaper the window draws with builds over the
// bundled collection.
func TestShaperBuilds(t *testing.T) {
	t.Parallel()
	if _, err := Shaper(); err != nil {
		t.Fatal(err)
	}
}

// TestAdvanceIsPositiveOnTheSample is a cheap shaping smoke check: the sample
// has real advance widths, so the faces shape rather than fall back silently.
func TestAdvanceIsPositiveOnTheSample(t *testing.T) {
	t.Parallel()
	src, err := data.ReadFile("data/Inter-Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := sfnt.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	buf := &sfnt.Buffer{}
	var total fixed.Int26_6
	for _, r := range "Fugue Việt Nam" {
		idx, err := f.GlyphIndex(buf, r)
		if err != nil {
			t.Fatalf("glyph index of %q: %v", r, err)
		}
		adv, err := f.GlyphAdvance(buf, idx, 16, font.HintingNone)
		if err != nil {
			t.Fatalf("advance of %q: %v", r, err)
		}
		total += adv
	}
	if total <= 0 {
		t.Errorf("sample advance = %v, want positive", total)
	}
}

func TestBundledBoldOutlinesDifferFromRegular(t *testing.T) {
	t.Parallel()
	for _, family := range []string{interFamily, "JetBrainsMono"} {
		regular := parseFont(t, "data/"+family+"-Regular.ttf")
		bold := parseFont(t, "data/"+family+"-Bold.ttf")
		width := func(f *sfnt.Font) fixed.Int26_6 {
			var buf sfnt.Buffer
			glyph, err := f.GlyphIndex(&buf, 'M')
			if err != nil {
				t.Fatal(err)
			}
			bounds, _, err := f.GlyphBounds(&buf, glyph, 16*64, font.HintingNone)
			if err != nil {
				t.Fatal(err)
			}
			return bounds.Max.X - bounds.Min.X
		}
		if width(regular) == width(bold) {
			t.Fatalf("%s bold outline has regular width", family)
		}
	}
}

func TestCollectionRegistersStaticWeightsAndStyles(t *testing.T) {
	t.Parallel()
	faces, err := Collection()
	if err != nil {
		t.Fatal(err)
	}
	want := map[giofont.Font]bool{
		{Typeface: interFamily, Weight: giofont.Normal, Style: giofont.Regular}:      false,
		{Typeface: interFamily, Weight: giofont.Medium, Style: giofont.Regular}:      false,
		{Typeface: interFamily, Weight: giofont.SemiBold, Style: giofont.Regular}:    false,
		{Typeface: interFamily, Weight: giofont.Bold, Style: giofont.Regular}:        false,
		{Typeface: interFamily, Weight: giofont.Normal, Style: giofont.Italic}:       false,
		{Typeface: "JetBrains Mono", Weight: giofont.Normal, Style: giofont.Regular}: false,
		{Typeface: "JetBrains Mono", Weight: giofont.Bold, Style: giofont.Regular}:   false,
	}
	for _, face := range faces {
		if _, ok := want[face.Font]; ok {
			want[face.Font] = true
		}
	}
	for face, found := range want {
		if !found {
			t.Errorf("missing font %+v", face)
		}
	}
}
