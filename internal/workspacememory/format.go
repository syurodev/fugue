package workspacememory

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// FormatVersion identifies the fugue-memory/1 Markdown exchange format (§8).
const FormatVersion = "fugue-memory/1"

const (
	entryOpenMarker  = "<!-- fugue:entry -->"
	entryCloseMarker = "<!-- /fugue:entry -->"
)

// ImportedEntry holds the parsed fields of one entry block in a fugue-memory/1 file.
type ImportedEntry struct {
	ID            string
	Kind          Kind
	Label         string
	Summary       string
	Body          string
	Keywords      []string
	Anchors       []Anchor
	Author        Author
	Verified      bool
	Validity      Validity
	InvalidReason string
	SupersededBy  string
	UpdatedAt     time.Time
	Revision      uint64
}

// DecodedBlock represents one parsed entry block or a per-block rejection reason.
type DecodedBlock struct {
	RawLabel     string
	Entry        *ImportedEntry
	RejectReason string
}

// DecodedFile represents a parsed fugue-memory/1 Markdown file.
type DecodedFile struct {
	Workspace  string
	ExportedAt time.Time
	Blocks     []DecodedBlock
}

type yamlFrontmatter struct {
	Format     string `yaml:"format"`
	Workspace  string `yaml:"workspace"`
	ExportedAt string `yaml:"exported_at"`
}

type yamlAnchor struct {
	Root string `yaml:"root"`
	Path string `yaml:"path"`
	Hash string `yaml:"hash"`
}

type yamlEntryMeta struct {
	ID            string       `yaml:"id"`
	Kind          Kind         `yaml:"kind"`
	Label         string       `yaml:"label"`
	Summary       string       `yaml:"summary"`
	Keywords      []string     `yaml:"keywords"`
	Anchors       []yamlAnchor `yaml:"anchors"`
	Author        Author       `yaml:"author"`
	Verified      *bool        `yaml:"verified"`
	Validity      Validity     `yaml:"validity"`
	InvalidReason string       `yaml:"invalid_reason"`
	SupersededBy  string       `yaml:"superseded_by"`
	UpdatedAt     string       `yaml:"updated_at"`
	Revision      uint64       `yaml:"revision"`
	BodyBytes     *int         `yaml:"body_bytes"`
}

// EncodeMemoryFile encodes workspace memory entries into the fugue-memory/1
// Markdown format (§8). Entries are sorted by ID for deterministic output and
// include invalidated and superseded entries; history is not exported.
//
// Body framing: each entry's yaml metadata fence includes `body_bytes: <n>`,
// recording the exact UTF-8 byte length of `body`. Immediately after the closing
// "```\n" of the yaml fence, EncodeMemoryFile writes those `<n>` bytes verbatim,
// followed by "\n<!-- /fugue:entry -->\n". Because the body is length-framed
// rather than delimiter-scanned, bodies containing marker comments, Markdown
// headings, code fences, CRLF sequences, or with/without a trailing newline
// round-trip byte for byte without ambiguity.
func EncodeMemoryFile(workspaceName string, exportedAt time.Time, entries []Entry) ([]byte, error) {
	fm := yamlFrontmatter{
		Format:     FormatVersion,
		Workspace:  workspaceName,
		ExportedAt: exportedAt.UTC().Format(time.RFC3339Nano),
	}
	fmBytes, err := yaml.Marshal(&fm)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal memory export frontmatter", err)
	}

	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b Entry) int {
		return strings.Compare(a.ID, b.ID)
	})

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---\n")

	for i := range sorted {
		if err := writeEncodedBlock(&buf, &sorted[i]); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func writeEncodedBlock(buf *bytes.Buffer, e *Entry) error {
	bodyLen := len(e.Body)
	verified := e.Verified
	anchors := make([]yamlAnchor, 0, len(e.Anchors))
	for _, a := range e.Anchors {
		anchors = append(anchors, yamlAnchor{
			Root: a.RootName,
			Path: a.RelPath,
			Hash: a.ContentHash,
		})
	}
	meta := yamlEntryMeta{
		ID:            e.ID,
		Kind:          e.Kind,
		Label:         e.Label,
		Summary:       e.Summary,
		Keywords:      nonNilKeywords(e.Keywords),
		Anchors:       anchors,
		Author:        e.Author,
		Verified:      &verified,
		Validity:      e.Validity,
		InvalidReason: e.InvalidReason,
		SupersededBy:  e.SupersededBy,
		UpdatedAt:     e.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Revision:      e.Revision,
		BodyBytes:     &bodyLen,
	}
	metaBytes, err := yaml.Marshal(&meta)
	if err != nil {
		return errcode.Wrap(errcode.Internal, fmt.Sprintf("marshal memory entry %s yaml", e.ID), err)
	}

	headingLabel := strings.ReplaceAll(strings.ReplaceAll(e.Label, "\r", " "), "\n", " ")
	buf.WriteByte('\n')
	buf.WriteString(entryOpenMarker)
	buf.WriteString("\n## ")
	buf.WriteString(headingLabel)
	buf.WriteString("\n\n```yaml\n")
	buf.Write(metaBytes)
	buf.WriteString("```\n")
	buf.WriteString(e.Body)
	buf.WriteByte('\n')
	buf.WriteString(entryCloseMarker)
	buf.WriteByte('\n')
	return nil
}
