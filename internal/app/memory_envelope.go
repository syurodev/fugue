package app

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/workspacememory"
)

const memoryEnvelopeNonceBytes = 4

// escapeEnvelopeValue escapes characters that could forge an attribute value
// or harness header line outside an untrusted envelope (content-safety §4.1).
func escapeEnvelopeValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\u000a`)
		case '\r':
			b.WriteString(`\u000d`)
		case '⟪':
			b.WriteString(`\u27ea`)
		case '⟫':
			b.WriteString(`\u27eb`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wrapMemoryEnvelope wraps untrusted memory content in a per-result nonce
// envelope with source=memory and entry attribute (content-safety §4.1, §4.4).
// Inner content is preserved verbatim so it is never altered or obfuscated.
func wrapMemoryEnvelope(random ids.Random, entryID, content string) (string, error) {
	if random == nil {
		random = ids.CryptoRandom{}
	}
	rawNonce := make([]byte, memoryEnvelopeNonceBytes)
	if err := random.Read(rawNonce); err != nil {
		return "", errcode.Wrap(errcode.Internal, "allocate memory envelope nonce", err)
	}
	nonce := hex.EncodeToString(rawNonce)
	var b strings.Builder
	b.WriteString("⟪untrusted ")
	b.WriteString(nonce)
	b.WriteString(` source=memory entry="`)
	b.WriteString(escapeEnvelopeValue(entryID))
	b.WriteString(`"⟫`)
	b.WriteByte('\n')
	b.WriteString(content)
	b.WriteByte('\n')
	b.WriteString("⟪/untrusted ")
	b.WriteString(nonce)
	b.WriteString("⟫")
	return b.String(), nil
}

func formatMemoryMeta(author workspacememory.Author, verified bool, freshness workspacememory.Freshness, revision uint64) string {
	return fmt.Sprintf(
		"verified=%s author=%s freshness=%s revision=%s",
		strconv.FormatBool(verified),
		escapeEnvelopeValue(string(author)),
		escapeEnvelopeValue(string(freshness)),
		strconv.FormatUint(revision, 10),
	)
}

func renderMemoryOverview(ov *workspacememory.OverviewResult, random ids.Random) (string, error) {
	budget := ov.BudgetBytes
	if budget <= 0 || budget > workspacememory.OverviewBudgetBytes {
		budget = workspacememory.OverviewBudgetBytes
	}
	blockTexts, err := renderOverviewBlocks(ov.Blocks, random)
	if err != nil {
		return "", err
	}
	lineTexts, err := renderOverviewLines(ov.IndexLines, random)
	if err != nil {
		return "", err
	}
	return assembleOverviewBudget(blockTexts, lineTexts, ov.OmittedCount, budget), nil
}

func renderOverviewBlocks(blocks []workspacememory.OverviewBlock, random ids.Random) ([]string, error) {
	out := make([]string, 0, len(blocks))
	for i := range blocks {
		blk := &blocks[i]
		header := fmt.Sprintf(
			"## %s (id=%s kind=pinned %s)",
			escapeEnvelopeValue(blk.Label),
			escapeEnvelopeValue(blk.ID),
			formatMemoryMeta(blk.Author, blk.Verified, blk.Freshness, blk.Revision),
		)
		content := blk.Summary + "\n" + blk.Body
		if !blk.Verified {
			wrapped, err := wrapMemoryEnvelope(random, blk.ID, content)
			if err != nil {
				return nil, err
			}
			content = wrapped
		}
		out = append(out, header+"\n"+content)
	}
	return out, nil
}

func renderOverviewLines(lines []workspacememory.OverviewIndexLine, random ids.Random) ([]string, error) {
	out := make([]string, 0, len(lines))
	for i := range lines {
		ln := &lines[i]
		header := fmt.Sprintf(
			"- %s (id=%s kind=%s %s)",
			escapeEnvelopeValue(ln.Label),
			escapeEnvelopeValue(ln.ID),
			escapeEnvelopeValue(string(ln.Kind)),
			formatMemoryMeta(ln.Author, ln.Verified, ln.Freshness, ln.Revision),
		)
		summary := ln.Summary
		if !ln.Verified {
			wrapped, err := wrapMemoryEnvelope(random, ln.ID, summary)
			if err != nil {
				return nil, err
			}
			summary = wrapped
		}
		out = append(out, header+"\n"+summary)
	}
	return out, nil
}

func assembleOverviewBudget(blocks, lines []string, baseOmitted, budget int) string {
	if len(blocks) == 0 && len(lines) == 0 && baseOmitted == 0 {
		return "Workspace memory is empty."
	}
	const blockHeader = "# Pinned Workspace Memory\n"
	const indexHeader = "# Workspace Memory Index\n"

	var keptBlocks, keptLines []string
	used := 0
	omitted := baseOmitted

	if len(blocks) > 0 {
		var dropped int
		keptBlocks, used, dropped = packOverviewSection(blocks, len(blockHeader), omitted+len(lines), budget)
		omitted += dropped
	}
	if len(lines) > 0 {
		overhead := len(indexHeader)
		if len(keptBlocks) > 0 {
			overhead++
		}
		var dropped int
		keptLines, _, dropped = packOverviewSection(lines, used+overhead, omitted, budget)
		omitted += dropped
	}
	return formatAssembledOverview(keptBlocks, keptLines, omitted, blockHeader, indexHeader)
}

func packOverviewSection(items []string, startUsed, otherRemaining, budget int) (kept []string, used, dropped int) {
	used = startUsed
	for i, item := range items {
		add := len(item) + 1
		tailReserve := len(overviewTailLine(otherRemaining + len(items) - i - 1))
		if used+add+tailReserve > budget {
			break
		}
		kept = append(kept, item)
		used += add
	}
	return kept, used, len(items) - len(kept)
}

func formatAssembledOverview(blocks, lines []string, omitted int, blockHeader, indexHeader string) string {
	var b strings.Builder
	if len(blocks) > 0 {
		b.WriteString(blockHeader)
		for _, blk := range blocks {
			b.WriteString(blk)
			b.WriteByte('\n')
		}
	}
	if len(lines) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(indexHeader)
		for _, ln := range lines {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	if tail := overviewTailLine(omitted); tail != "" {
		b.WriteString(tail)
	}
	return strings.TrimRight(b.String(), "\n")
}

func overviewTailLine(omitted int) string {
	if omitted <= 0 {
		return ""
	}
	return fmt.Sprintf("\n%d more entries, use %s\n", omitted, memorySearchToolName)
}
