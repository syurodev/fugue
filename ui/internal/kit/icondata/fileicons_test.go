package icondata

import (
	"embed"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// symbolsFiles embeds the vendored Symbols SVG sources and their reduced
// mapping, with the MIT license next to the data.
//
//go:embed testdata/symbols/mapping.json testdata/symbols/files/*.svg testdata/symbols/folders/*.svg
var symbolsFiles embed.FS

// TestFileIconDataIsCurrentAndArchIndependent is the file-icon generator
// committed with its output: it converts the vendored Symbols SVGs to the
// kit's M/L/C/Z path data and checks fileicons.go against the result. The
// conversion rounds every coordinate to a thousandth of the 24 grid and uses
// explicit float64 arithmetic for every product that feeds an addition, so
// the output is identical on arm64 and amd64.
func TestFileIconDataIsCurrentAndArchIndependent(t *testing.T) {
	t.Parallel()
	const path = "fileicons.go"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	first := composeFileIcons(t)
	if second := composeFileIcons(t); second != first {
		t.Fatal("the generator is not deterministic")
	}
	if want := first; string(got) != want {
		t.Fatalf("%s is stale against testdata/symbols; regenerate with:\n\tgo test -tags regenerate ./internal/kit/icondata -run TestRegenerateFileIconData", path)
	}
}

// symbolsMapping is the reduced upstream mapping: only icons that are shipped
// appear in its definitions, and every file/folder entry points at one.
type symbolsMapping struct {
	IconDefinitions map[string]struct {
		IconPath string `json:"iconPath"`
	} `json:"iconDefinitions"`
	FileNames       map[string]string `json:"fileNames"`
	FileExtensions  map[string]string `json:"fileExtensions"`
	FolderNames     map[string]string `json:"folderNames"`
	RootFolderNames map[string]string `json:"rootFolderNames"`
	File            string            `json:"file"`
	Folder          string            `json:"folder"`
}

// symbolsIcon is one converted glyph.
type symbolsIcon struct {
	Fill        string
	FillEvenOdd string
	Stroke      string
	Color       string
	StrokeWidth float32
	LineCap     string
	LineJoin    string
}

// symbolsColorName maps the single source hex of an icon to the small set of
// theme color names (ui-kit §6.1). An unknown hex fails the generator instead
// of silently picking a color.
func symbolsColorName(hex string) (string, bool) {
	switch hex {
	case "#64748b":
		return "gray", true
	case "#60a5fa", "#2563eb", "#0ea5e9", "#38bdf8":
		return "blue", true
	case "#f87171":
		return "red", true
	case "#ea580c":
		return "orange", true
	case "#f59e0b", "#fbbf24":
		return "yellow", true
	case "#22c55e", "#16a34a":
		return "green", true
	case "#14b8a6", "#2dd4bf":
		return "teal", true
	case "#a855f7", "#8b5cf6", "#c084fc", "#a78bfa":
		return "violet", true
	case "#f472b6":
		return "pink", true
	default:
		return "", false
	}
}

// composeFileIcons converts every shipped Symbols icon and composes the whole
// content of fileicons.go.
func composeFileIcons(t *testing.T) string {
	t.Helper()
	m := loadSymbolsMapping(t)
	names := make([]string, 0, len(m.IconDefinitions))
	for name := range m.IconDefinitions {
		names = append(names, name)
	}
	sort.Strings(names)
	icons := make(map[string]symbolsIcon, len(names))
	for _, name := range names {
		icons[name] = convertSymbolsIcon(t, name, m.IconDefinitions[name].IconPath)
	}
	return writeFileIconsGo(m, names, icons)
}

func loadSymbolsMapping(t *testing.T) symbolsMapping {
	t.Helper()
	raw, err := fs.ReadFile(symbolsFiles, "testdata/symbols/mapping.json")
	if err != nil {
		t.Fatalf("read the Symbols mapping: %v", err)
	}
	var m symbolsMapping
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse the Symbols mapping: %v", err)
	}
	m.FileNames = lowerKeys(t, "fileNames", m.FileNames)
	m.FileExtensions = lowerKeys(t, "fileExtensions", m.FileExtensions)
	m.FolderNames = lowerKeys(t, "folderNames", m.FolderNames)
	m.RootFolderNames = lowerKeys(t, "rootFolderNames", m.RootFolderNames)
	for _, table := range []map[string]string{m.FileNames, m.FileExtensions, m.FolderNames, m.RootFolderNames} {
		for k, icon := range table {
			requireMapped(t, m, k, icon)
		}
	}
	return m
}

func writeFileIconsGo(m symbolsMapping, names []string, icons map[string]symbolsIcon) string {
	var b strings.Builder
	b.WriteString(`// Code generated from the Symbols SVG data in testdata/symbols; DO NOT EDIT.
// MIT license at LICENSE-symbols.txt. Regenerate with:
//
//	go test -tags regenerate ./internal/kit/icondata -run TestRegenerateFileIconData

package icondata

// FileIcon is one Symbols glyph: path data on the 24 grid, the theme color
// name, and how the kit paints it. FillEvenOdd carries the glyph's evenodd
// subpaths; Stroke carries the stroked ones.
type FileIcon struct {
	Fill        string
	FillEvenOdd string
	Stroke      string
	Color       string
	StrokeWidth float32
	LineCap     string
	LineJoin    string
}

// FileIcons holds the packaged Symbols glyphs by upstream name.
var FileIcons = map[string]FileIcon{
`)
	for _, name := range names {
		ic := icons[name]
		fmt.Fprintf(&b, "\t%q: {Fill: %q, FillEvenOdd: %q, Stroke: %q, Color: %q, StrokeWidth: %s, LineCap: %q, LineJoin: %q},\n",
			name, ic.Fill, ic.FillEvenOdd, ic.Stroke, ic.Color, formatFloat32(ic.StrokeWidth), ic.LineCap, ic.LineJoin)
	}
	b.WriteString("}\n\n")
	writeStringMap(&b, "FileNames", m.FileNames)
	writeStringMap(&b, "FileExtensions", m.FileExtensions)
	writeStringMap(&b, "FolderNames", m.FolderNames)
	fmt.Fprintf(&b, "// FileGeneric is the generic file icon; FolderGeneric is the generic folder icon.\nconst (\n\tFileGeneric = %q\n\tFolderGeneric = %q\n)\n", m.File, m.Folder)
	return b.String()
}

// lowerKeys normalizes one mapping section to the lower-case keys the kit
// looks up. A case collision that points at different icons would make the
// generated table depend on map order, so it fails the generator instead.
func lowerKeys(t *testing.T, section string, in map[string]string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(in))
	for k, v := range in {
		lk := strings.ToLower(k)
		if prev, ok := out[lk]; ok && prev != v {
			t.Fatalf("mapping %s: %q and a differently cased key point at %q and %q", section, k, prev, v)
		}
		out[lk] = v
	}
	return out
}

// requireMapped fails when a mapping entry points at an icon that is not
// shipped.
func requireMapped(t *testing.T, m symbolsMapping, key, icon string) {
	t.Helper()
	if _, ok := m.IconDefinitions[icon]; !ok {
		t.Fatalf("mapping %q points at %q, which is not a shipped icon", key, icon)
	}
}

// mapKeyName describes one lookup table's keys in its generated comment.
func mapKeyName(name string) string {
	switch name {
	case "FileNames":
		return "lower-case full file name"
	case "FileExtensions":
		return "lower-case file extension"
	default:
		return "lower-case folder name"
	}
}

// writeStringMap writes one generated lookup table with sorted keys.
func writeStringMap(b *strings.Builder, name string, m map[string]string) {
	fmt.Fprintf(b, "// %s maps a %s to a Symbols icon.\nvar %s = map[string]string{\n", name, mapKeyName(name), name)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "\t%q: %q,\n", k, m[k])
	}
	b.WriteString("}\n\n")
}

// convertSymbolsIcon reads one SVG and converts it to the generated glyph.
func convertSymbolsIcon(t *testing.T, name, iconPath string) symbolsIcon {
	t.Helper()
	rel := "testdata/symbols/" + strings.TrimPrefix(iconPath, "./icons/")
	data, err := fs.ReadFile(symbolsFiles, rel)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	ic, reason := parseSymbolsSVG(data)
	if reason != "" {
		t.Fatalf("%s: %s", name, reason)
	}
	return ic
}

// svgPaint is the inherited paint state of an SVG group.
type svgPaint struct {
	fill, stroke, fillRule, lineCap, lineJoin string
	strokeWidth                               float32
}

// svgClip is one clip rectangle from a clipPath definition.
type svgClip struct {
	x, y, w, h float64
	ok         bool
}

const (
	svgNone    = "none"
	svgRect    = "rect"
	svgPolygon = "polygon"
)

type svgState struct {
	data         []byte
	paint        svgPaint
	clips        map[string]svgClip
	clipStack    []string
	fill         []rawSubpath
	fillEvenOdd  []rawSubpath
	stroke       []rawSubpath
	colors       []string
	strokeWidths []float32
	lineCaps     []string
	lineJoins    []string
	clipUsed     string
	skip         string
}

// parseSymbolsSVG parses one Symbols SVG. It returns a reason instead of an
// icon when the file uses a feature the kit does not draw.
func parseSymbolsSVG(data []byte) (symbolsIcon, string) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	st := svgState{
		data:  data,
		paint: svgPaint{fill: svgNone, stroke: svgNone},
		clips: make(map[string]svgClip),
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if reason := st.handleStart(dec, t); reason != "" {
				return symbolsIcon{}, reason
			}
		case xml.EndElement:
			if t.Name.Local == "g" && len(st.clipStack) > 0 {
				st.clipStack = st.clipStack[:len(st.clipStack)-1]
			}
		}
	}
	return st.finalize()
}

func (st *svgState) handleStart(dec *xml.Decoder, t xml.StartElement) string {
	switch t.Name.Local {
	case "svg":
		st.paint = applyPaint(&st.paint, t)
	case "defs":
		if err := skipElement(dec, t.Name.Local); err != nil {
			return "bad defs: " + err.Error()
		}
		st.clips = collectClips(st.data)
	case "g":
		if _, ok := svgAttr(t, "transform"); ok {
			st.skip = "uses an element transform"
		}
		if id, ok := svgAttr(t, "clip-path"); ok {
			st.clipStack = append(st.clipStack, clipID(id))
		}
		st.paint = applyPaint(&st.paint, t)
	case "path", svgRect, "circle", "ellipse", "line", svgPolygon, "polyline":
		return st.handleShape(t)
	case "style", "mask", "filter", "use":
		st.skip = "uses a style, mask, filter or use element"
	}
	return ""
}

func (st *svgState) handleShape(t xml.StartElement) string {
	if st.skip != "" {
		return ""
	}
	if _, ok := svgAttr(t, "transform"); ok {
		st.skip = "uses an element transform"
		return ""
	}
	d, ok := shapePath(t)
	if !ok {
		st.skip = "uses a shape the generator does not convert"
		return ""
	}
	sp := applyPaint(&st.paint, t)
	if c := clipStackTop(st.clipStack); c != "" {
		st.clipUsed = c
	}
	var p rawParser
	if err := p.run(d); err != nil {
		return "bad shape path: " + err.Error()
	}
	st.recordShape(&sp, p.out)
	return ""
}

func (st *svgState) recordShape(sp *svgPaint, out []rawSubpath) {
	if sp.fill != svgNone {
		st.colors = append(st.colors, sp.fill)
		if sp.fillRule == "evenodd" {
			st.fillEvenOdd = append(st.fillEvenOdd, out...)
		} else {
			st.fill = append(st.fill, out...)
		}
	}
	if sp.stroke != svgNone {
		st.colors = append(st.colors, sp.stroke)
		st.stroke = append(st.stroke, out...)
		st.strokeWidths = append(st.strokeWidths, sp.strokeWidth)
		st.lineCaps = append(st.lineCaps, sp.lineCap)
		st.lineJoins = append(st.lineJoins, sp.lineJoin)
	}
}

func (st *svgState) finalize() (symbolsIcon, string) {
	if st.skip != "" {
		return symbolsIcon{}, st.skip
	}
	colorName, reason := st.resolveColor()
	if reason != "" {
		return symbolsIcon{}, reason
	}
	if clipReason := st.checkClip(); clipReason != "" {
		return symbolsIcon{}, clipReason
	}
	width, cap, join, reason := oneStroke(st.strokeWidths, st.lineCaps, st.lineJoins)
	if reason != "" {
		return symbolsIcon{}, reason
	}
	return symbolsIcon{
		Fill:        encode(st.fill),
		FillEvenOdd: encode(st.fillEvenOdd),
		Stroke:      encode(st.stroke),
		Color:       colorName,
		StrokeWidth: width,
		LineCap:     cap,
		LineJoin:    join,
	}, ""
}

func (st *svgState) resolveColor() (string, string) {
	if len(st.colors) == 0 {
		return "", "has no painted shape"
	}
	col := strings.ToLower(st.colors[0])
	for _, c := range st.colors[1:] {
		if c != col {
			return "", "uses more than one color"
		}
	}
	name, ok := symbolsColorName(col)
	if !ok {
		return "", "uses the color " + col + ", which has no theme name"
	}
	return name, ""
}

func (st *svgState) checkClip() string {
	if st.clipUsed == "" {
		return ""
	}
	c, ok := st.clips[st.clipUsed]
	if !ok || !c.ok {
		return "uses a clip-path the generator cannot read"
	}
	minX, minY, maxX, maxY := subpathsBounds(st.fill, st.fillEvenOdd, st.stroke)
	if !c.contains(minX, minY, maxX, maxY) {
		return fmt.Sprintf("uses a clip-path that is not the full frame (bounds %.5f %.5f %.5f %.5f, clip %+v)", minX, minY, maxX, maxY, c)
	}
	return ""
}

// oneStroke returns the single stroke style of an icon, or a reason when the
// paths disagree.
func oneStroke(widths []float32, caps, joins []string) (float32, string, string, string) {
	if len(widths) == 0 {
		return 0, "", "", ""
	}
	width, cap, join := widths[0], caps[0], joins[0]
	for i := range widths {
		if widths[i] != width || caps[i] != cap || joins[i] != join {
			return 0, "", "", "uses more than one stroke style"
		}
	}
	return width, cap, join, ""
}

// applyPaint copies the shape's paint attributes over the inherited state.
func applyPaint(base *svgPaint, t xml.StartElement) svgPaint {
	p := *base
	if v, ok := svgAttr(t, "fill"); ok {
		p.fill = normalizePaint(v)
	}
	if v, ok := svgAttr(t, "stroke"); ok {
		p.stroke = normalizePaint(v)
	}
	if v, ok := svgAttr(t, "fill-rule"); ok {
		p.fillRule = v
	}
	if v, ok := svgAttr(t, "stroke-linecap"); ok {
		p.lineCap = v
	}
	if v, ok := svgAttr(t, "stroke-linejoin"); ok {
		p.lineJoin = v
	}
	if v, ok := svgAttr(t, "stroke-width"); ok {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			p.strokeWidth = float32(n)
		}
	}
	return p
}

// normalizePaint lower-cases a paint value and expands #RGB.
func normalizePaint(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "black" {
		return "#000000"
	}
	if v == "white" {
		return "#ffffff"
	}
	if strings.HasPrefix(v, "#") && len(v) == 4 {
		return "#" + v[1:2] + v[1:2] + v[2:3] + v[2:3] + v[3:4] + v[3:4]
	}
	return v
}

// clipID extracts the id from url(#id).
func clipID(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "url(#") && strings.HasSuffix(v, ")") {
		return strings.TrimSuffix(strings.TrimPrefix(v, "url(#"), ")")
	}
	return ""
}

// clipStackTop returns the innermost clip id.
func clipStackTop(stack []string) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1]
}

// svgAttr returns the value of one attribute.
func svgAttr(t xml.StartElement, name string) (string, bool) {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// skipElement consumes the rest of a defs subtree.
func skipElement(dec *xml.Decoder, name string) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode svg token: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == name {
				depth++
			}
		case xml.EndElement:
			if t.Name.Local == name {
				depth--
			}
		}
	}
	return nil
}

// collectClips reads the clipPath rectangles of one SVG. A clipPath with
// anything but a translated rectangle is marked unreadable.
func collectClips(data []byte) map[string]svgClip {
	clips := make(map[string]svgClip)
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var id string
	var current svgClip
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return clips
		}
		switch t := tok.(type) {
		case xml.StartElement:
			id, current, depth = clipStart(t, id, current, depth)
		case xml.EndElement:
			if depth > 0 {
				depth--
				if depth == 0 {
					clips[id] = current
				}
			}
		}
	}
}

func clipStart(t xml.StartElement, id string, current svgClip, depth int) (string, svgClip, int) {
	if t.Name.Local == "clipPath" {
		newID, _ := svgAttr(t, "id")
		return newID, svgClip{}, 1
	}
	if depth <= 0 {
		return id, current, depth
	}
	if t.Name.Local == svgRect {
		return id, readClipRect(t), depth + 1
	}
	current.ok = false
	return id, current, depth + 1
}

// readClipRect reads one clip rectangle, accepting only a translate.
func readClipRect(t xml.StartElement) svgClip {
	x, _ := svgFloat(t, "x")
	y, _ := svgFloat(t, "y")
	w, okW := svgFloat(t, "width")
	h, okH := svgFloat(t, "height")
	if !okW || !okH {
		return svgClip{}
	}
	if v, ok := svgAttr(t, "transform"); ok {
		dx, dy, ok := parseTranslate(v)
		if !ok {
			return svgClip{}
		}
		x, y = float64(x)+float64(dx), float64(y)+float64(dy)
	}
	return svgClip{x: x, y: y, w: w, h: h, ok: true}
}

// parseTranslate reads translate(x y) or translate(x,y).
func parseTranslate(v string) (float64, float64, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "translate(") || !strings.HasSuffix(v, ")") {
		return 0, 0, false
	}
	parts := strings.FieldsFunc(strings.TrimSuffix(strings.TrimPrefix(v, "translate("), ")"), func(r rune) bool {
		return r == ',' || r == ' '
	})
	if len(parts) != 1 && len(parts) != 2 {
		return 0, 0, false
	}
	x, errX := strconv.ParseFloat(parts[0], 64)
	var y float64
	var errY error
	if len(parts) == 2 {
		y, errY = strconv.ParseFloat(parts[1], 64)
	}
	return x, y, errX == nil && errY == nil
}

// svgFloat reads one numeric attribute.
func svgFloat(t xml.StartElement, name string) (float64, bool) {
	v, ok := svgAttr(t, name)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return n, err == nil
}

// shapePath converts one SVG shape to absolute path data the raw parser
// understands.
func shapePath(t xml.StartElement) (string, bool) {
	switch t.Name.Local {
	case "path":
		d, ok := svgAttr(t, "d")
		return d, ok && d != ""
	case svgRect:
		return rectPath(t)
	case "circle":
		return circlePath(t)
	case "ellipse":
		return ellipseElemPath(t)
	case "line":
		return linePath(t)
	case svgPolygon, "polyline":
		return polyPath(t, t.Name.Local == svgPolygon)
	}
	return "", false
}

func circlePath(t xml.StartElement) (string, bool) {
	cx, okCX := svgFloat(t, "cx")
	cy, okCY := svgFloat(t, "cy")
	r, okR := svgFloat(t, "r")
	if !okCX || !okCY || !okR {
		return "", false
	}
	return ellipsePath(cx, cy, r, r), true
}

func ellipseElemPath(t xml.StartElement) (string, bool) {
	cx, okCX := svgFloat(t, "cx")
	cy, okCY := svgFloat(t, "cy")
	rx, okRX := svgFloat(t, "rx")
	ry, okRY := svgFloat(t, "ry")
	if !okCX || !okCY || !okRX || !okRY {
		return "", false
	}
	return ellipsePath(cx, cy, rx, ry), true
}

func linePath(t xml.StartElement) (string, bool) {
	x1, ok1 := svgFloat(t, "x1")
	y1, ok2 := svgFloat(t, "y1")
	x2, ok3 := svgFloat(t, "x2")
	y2, ok4 := svgFloat(t, "y2")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return "", false
	}
	return "M" + num(x1) + " " + num(y1) + "L" + num(x2) + " " + num(y2), true
}

func polyPath(t xml.StartElement, closed bool) (string, bool) {
	pts, ok := svgAttr(t, "points")
	if !ok {
		return "", false
	}
	fields := strings.FieldsFunc(pts, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	if len(fields) < 4 || len(fields)%2 != 0 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(fields); i += 2 {
		if i == 0 {
			b.WriteString("M")
		} else {
			b.WriteString("L")
		}
		b.WriteString(fields[i] + " " + fields[i+1])
	}
	if closed {
		b.WriteString("Z")
	}
	return b.String(), true
}

// rectPath converts a rect, rounded when rx or ry is set.
func rectPath(t xml.StartElement) (string, bool) {
	x, _ := svgFloat(t, "x")
	y, _ := svgFloat(t, "y")
	w, okW := svgFloat(t, "width")
	h, okH := svgFloat(t, "height")
	if !okW || !okH {
		return "", false
	}
	rx, _ := svgFloat(t, "rx")
	ry, _ := svgFloat(t, "ry")
	if rx == 0 {
		rx = ry
	}
	if ry == 0 {
		ry = rx
	}
	if rx <= 0 || ry <= 0 {
		return "M" + num(x) + " " + num(y) + "H" + num(float64(x)+float64(w)) +
			"V" + num(float64(y)+float64(h)) + "H" + num(x) + "Z", true
	}
	right := float64(x) + float64(w)
	bottom := float64(y) + float64(h)
	return "M" + num(float64(x)+float64(rx)) + " " + num(y) +
		"H" + num(float64(right)-float64(rx)) +
		"A" + num(rx) + " " + num(ry) + " 0 0 1 " + num(right) + " " + num(float64(y)+float64(ry)) +
		"V" + num(float64(bottom)-float64(ry)) +
		"A" + num(rx) + " " + num(ry) + " 0 0 1 " + num(float64(right)-float64(rx)) + " " + num(bottom) +
		"H" + num(float64(x)+float64(rx)) +
		"A" + num(rx) + " " + num(ry) + " 0 0 1 " + num(x) + " " + num(float64(bottom)-float64(ry)) +
		"V" + num(float64(y)+float64(ry)) +
		"A" + num(rx) + " " + num(ry) + " 0 0 1 " + num(float64(x)+float64(rx)) + " " + num(y) + "Z", true
}

// ellipsePath builds a full ellipse from two arcs.
func ellipsePath(cx, cy, rx, ry float64) string {
	return "M" + num(float64(cx)-float64(rx)) + " " + num(cy) +
		"A" + num(rx) + " " + num(ry) + " 0 1 0 " + num(float64(cx)+float64(rx)) + " " + num(cy) +
		"A" + num(rx) + " " + num(ry) + " 0 1 0 " + num(float64(cx)-float64(rx)) + " " + num(cy) + "Z"
}

// num formats a coordinate for the raw parser.
func num(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// subpathsBounds returns the bounding box of every point of the glyph.
func subpathsBounds(groups ...[]rawSubpath) (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	add := func(p rawPoint) {
		minX = math.Min(minX, float64(p.X))
		minY = math.Min(minY, float64(p.Y))
		maxX = math.Max(maxX, float64(p.X))
		maxY = math.Max(maxY, float64(p.Y))
	}
	for _, subs := range groups {
		for _, sp := range subs {
			add(sp.Move)
			for _, seg := range sp.Segs {
				if seg.IsCubic {
					add(seg.C0)
					add(seg.C1)
					add(seg.End)
				} else {
					add(seg.To)
				}
			}
		}
	}
	return minX, minY, maxX, maxY
}

// contains reports whether the clip rectangle covers the glyph bounds. The
// source exports sometimes carry a content-bounds clip that misses the ink by
// a fraction of a grid unit (Go's band, for example); ignoring it changes no
// visible pixel at badge sizes, while a real clip stays rejected.
func (c svgClip) contains(minX, minY, maxX, maxY float64) bool {
	const eps = 0.1
	return c.x <= minX+eps && c.y <= minY+eps &&
		float64(c.x)+float64(c.w) >= maxX-eps && float64(c.y)+float64(c.h) >= maxY-eps
}

// formatFloat32 writes a float32 literal without a trailing zero fraction.
func formatFloat32(v float32) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}
