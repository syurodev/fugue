package workspacememory

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// DecodeMemoryFile parses a fugue-memory/1 Markdown file (§8).
// Invalid UTF-8, broken frontmatter, or an unsupported format version fails the
// entire decode with errcode.MemoryImportInvalid. A broken individual entry
// block is recorded as a DecodedBlock with RejectReason so remaining blocks can
// still be previewed and imported.
func DecodeMemoryFile(content []byte) (DecodedFile, error) {
	if !utf8.Valid(content) {
		return DecodedFile{}, errcode.New(errcode.MemoryImportInvalid, "memory import file is not valid UTF-8")
	}
	fm, restStart, err := parseFrontmatter(content)
	if err != nil {
		return DecodedFile{}, err
	}
	exportedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(fm.ExportedAt))
	if err != nil {
		return DecodedFile{}, errcode.New(errcode.MemoryImportInvalid, "memory import frontmatter exported_at must be RFC 3339")
	}

	blocks, err := decodeBlocks(content, restStart)
	if err != nil {
		return DecodedFile{}, err
	}
	return DecodedFile{
		Workspace:  fm.Workspace,
		ExportedAt: exportedAt.UTC(),
		Blocks:     blocks,
	}, nil
}

func parseFrontmatter(content []byte) (yamlFrontmatter, int, error) {
	if !bytes.HasPrefix(content, []byte("---\n")) {
		return yamlFrontmatter{}, 0, errcode.New(errcode.MemoryImportInvalid, "memory import file must begin with YAML frontmatter (---)")
	}
	fmStart := len("---\n")
	idx := bytes.Index(content[fmStart:], []byte("\n---\n"))
	endOffset := 0
	if idx >= 0 {
		endOffset = fmStart + idx + len("\n---\n")
	} else if bytes.HasSuffix(content[fmStart:], []byte("\n---")) {
		idx = len(content[fmStart:]) - len("\n---")
		endOffset = len(content)
	} else if bytes.Equal(content[fmStart:], []byte("---")) {
		idx = 0
		endOffset = len(content)
	} else {
		return yamlFrontmatter{}, 0, errcode.New(errcode.MemoryImportInvalid, "unterminated YAML frontmatter")
	}

	fmRaw := content[fmStart : fmStart+idx]
	if len(bytes.TrimSpace(fmRaw)) == 0 {
		return yamlFrontmatter{}, 0, errcode.New(errcode.MemoryImportInvalid, "empty YAML frontmatter")
	}
	var fm yamlFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader(fmRaw))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return yamlFrontmatter{}, 0, errcode.New(errcode.MemoryImportInvalid, "invalid YAML frontmatter")
	}
	if fm.Format != FormatVersion {
		return yamlFrontmatter{}, 0, errcode.New(errcode.MemoryImportInvalid, "unsupported memory export format version")
	}
	return fm, endOffset, nil
}

func decodeBlocks(content []byte, pos int) ([]DecodedBlock, error) {
	var blocks []DecodedBlock
	for pos < len(content) {
		next, found, err := advanceToNextBlock(content, pos)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		block, after := decodeOneBlock(content, next)
		blocks = append(blocks, block)
		pos = after
	}
	return blocks, nil
}

func advanceToNextBlock(content []byte, pos int) (int, bool, error) {
	for pos < len(content) {
		lineEnd := bytes.IndexByte(content[pos:], '\n')
		var line []byte
		var nextPos int
		if lineEnd < 0 {
			line = content[pos:]
			nextPos = len(content)
		} else {
			line = content[pos : pos+lineEnd]
			nextPos = pos + lineEnd + 1
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			pos = nextPos
			continue
		}
		if trimmed == entryOpenMarker {
			return nextPos, true, nil
		}
		return 0, false, errcode.New(errcode.MemoryImportInvalid, "unexpected content outside entry blocks")
	}
	return pos, false, nil
}

func decodeOneBlock(content []byte, blockStart int) (DecodedBlock, int) {
	rawLabel, yamlFenceStart, ok := findBlockHeadingAndYAMLFence(content, blockStart)
	if !ok {
		return DecodedBlock{
			RawLabel:     rawLabel,
			RejectReason: "entry block is missing heading or yaml metadata fence",
		}, recoverAfterFailedBlock(content, blockStart)
	}
	yamlCloseIdx := bytes.Index(content[yamlFenceStart:], []byte("\n```\n"))
	if yamlCloseIdx < 0 {
		return DecodedBlock{
			RawLabel:     rawLabel,
			RejectReason: "entry block yaml fence is unterminated",
		}, recoverAfterFailedBlock(content, yamlFenceStart)
	}
	yamlBytes := content[yamlFenceStart : yamlFenceStart+yamlCloseIdx]
	bodyStart := yamlFenceStart + yamlCloseIdx + len("\n```\n")

	meta, errMsg := parseEntryYAML(yamlBytes)
	if errMsg != "" {
		return DecodedBlock{
			RawLabel:     rawLabel,
			RejectReason: errMsg,
		}, recoverAfterFailedBlock(content, bodyStart)
	}
	bodyBytes := *meta.BodyBytes
	if bodyBytes < 0 || bodyBytes > len(content)-bodyStart {
		return DecodedBlock{
			RawLabel:     rawLabel,
			RejectReason: "entry body_bytes exceeds remaining file length",
		}, recoverAfterFailedBlock(content, bodyStart)
	}
	body := string(content[bodyStart : bodyStart+bodyBytes])
	afterBody := bodyStart + bodyBytes
	endPos, closed := consumeClosingMarker(content, afterBody)
	if !closed {
		return DecodedBlock{
			RawLabel:     rawLabel,
			RejectReason: "missing closing <!-- /fugue:entry --> marker at body_bytes boundary",
		}, recoverAfterFailedBlock(content, bodyStart)
	}

	entry, entryErr := buildImportedEntry(&meta, body)
	if entryErr != "" {
		return DecodedBlock{
			RawLabel:     rawLabel,
			RejectReason: entryErr,
		}, endPos
	}
	return DecodedBlock{
		RawLabel: rawLabel,
		Entry:    entry,
	}, endPos
}

func findBlockHeadingAndYAMLFence(content []byte, pos int) (rawLabel string, yamlStart int, ok bool) {
	sawHeading := false
	for pos < len(content) {
		lineEnd := bytes.IndexByte(content[pos:], '\n')
		if lineEnd < 0 {
			return rawLabel, 0, false
		}
		line := string(content[pos : pos+lineEnd])
		nextPos := pos + lineEnd + 1
		if line == entryOpenMarker || line == entryCloseMarker {
			return rawLabel, 0, false
		}
		if line == "```yaml" {
			return rawLabel, nextPos, sawHeading
		}
		if strings.HasPrefix(line, "## ") && !sawHeading {
			rawLabel = strings.TrimPrefix(line, "## ")
			sawHeading = true
			pos = nextPos
			continue
		}
		if strings.TrimSpace(line) == "" {
			pos = nextPos
			continue
		}
		return rawLabel, 0, false
	}
	return rawLabel, 0, false
}

func parseEntryYAML(yamlBytes []byte) (yamlEntryMeta, string) {
	if len(bytes.TrimSpace(yamlBytes)) == 0 {
		return yamlEntryMeta{}, "entry yaml metadata block is empty"
	}
	var meta yamlEntryMeta
	dec := yaml.NewDecoder(bytes.NewReader(yamlBytes))
	dec.KnownFields(true)
	if err := dec.Decode(&meta); err != nil {
		return yamlEntryMeta{}, "entry yaml metadata is invalid or contains unknown fields"
	}
	if meta.BodyBytes == nil {
		return yamlEntryMeta{}, "entry yaml metadata is missing body_bytes"
	}
	if meta.Verified == nil {
		return yamlEntryMeta{}, "entry yaml metadata is missing verified flag"
	}
	return meta, ""
}

func consumeClosingMarker(content []byte, pos int) (int, bool) {
	if !bytes.HasPrefix(content[pos:], []byte("\n"+entryCloseMarker)) {
		return pos, false
	}
	after := pos + len("\n"+entryCloseMarker)
	if after == len(content) {
		return after, true
	}
	if content[after] == '\n' {
		return after + 1, true
	}
	if bytes.HasPrefix(content[after:], []byte("\r\n")) {
		return after + 2, true
	}
	return pos, false
}

func recoverAfterFailedBlock(content []byte, pos int) int {
	for pos < len(content) {
		lineEnd := bytes.IndexByte(content[pos:], '\n')
		var line string
		var nextPos int
		if lineEnd < 0 {
			line = string(content[pos:])
			nextPos = len(content)
		} else {
			line = string(content[pos : pos+lineEnd])
			nextPos = pos + lineEnd + 1
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == entryCloseMarker {
			return nextPos
		}
		if trimmed == entryOpenMarker {
			return pos
		}
		pos = nextPos
	}
	return len(content)
}

func buildImportedEntry(meta *yamlEntryMeta, body string) (*ImportedEntry, string) {
	if strings.TrimSpace(meta.ID) == "" {
		return nil, "entry id is required"
	}
	if !validKind(meta.Kind) {
		return nil, "invalid workspace memory kind"
	}
	if !validAuthor(meta.Author) {
		return nil, "invalid workspace memory author"
	}
	if !validValidity(meta.Validity) {
		return nil, "invalid workspace memory validity"
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(meta.UpdatedAt))
	if err != nil {
		return nil, "entry updated_at must be RFC 3339"
	}
	anchors := make([]Anchor, 0, len(meta.Anchors))
	for _, a := range meta.Anchors {
		if strings.TrimSpace(a.Root) == "" || strings.TrimSpace(a.Path) == "" {
			return nil, "anchor root and path are required"
		}
		anchors = append(anchors, Anchor{
			RootName:    a.Root,
			RelPath:     a.Path,
			ContentHash: a.Hash,
		})
	}
	return &ImportedEntry{
		ID:            meta.ID,
		Kind:          meta.Kind,
		Label:         meta.Label,
		Summary:       meta.Summary,
		Body:          body,
		Keywords:      nonNilKeywords(meta.Keywords),
		Anchors:       anchors,
		Author:        meta.Author,
		Verified:      *meta.Verified,
		Validity:      meta.Validity,
		InvalidReason: meta.InvalidReason,
		SupersededBy:  meta.SupersededBy,
		UpdatedAt:     updatedAt.UTC(),
		Revision:      meta.Revision,
	}, ""
}
