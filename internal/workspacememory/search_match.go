package workspacememory

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	ftsWordsTable   = "workspace_memory_fts_words"
	ftsTrigramTable = "workspace_memory_fts_trigram"
)

func (s *Store) queryWordFTS(
	ctx context.Context,
	workspaceID string,
	kind Kind,
	matchQuery string,
	out map[string]*rawSearchCandidate,
) error {
	return s.queryFTSTable(ctx, ftsWordsTable, true, workspaceID, kind, matchQuery, out)
}

func (s *Store) queryTrigramFTS(
	ctx context.Context,
	workspaceID string,
	kind Kind,
	matchQuery string,
	out map[string]*rawSearchCandidate,
) error {
	return s.queryFTSTable(ctx, ftsTrigramTable, false, workspaceID, kind, matchQuery, out)
}

func (s *Store) queryFTSTable(
	ctx context.Context,
	tableName string,
	isWord bool,
	workspaceID string,
	kind Kind,
	matchQuery string,
	out map[string]*rawSearchCandidate,
) error {
	query, args := buildFTSQuery(tableName, workspaceID, kind, matchQuery)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "query workspace memory FTS index", err)
	}
	defer closeRows(ctx, s.log, rows)

	for rows.Next() {
		row, bm25Val, scanErr := scanFTSRow(rows)
		if scanErr != nil {
			return scanErr
		}
		recordFTSCandidate(out, &row, normalizeBM25(bm25Val), isWord)
	}
	if err := rows.Err(); err != nil {
		return errcode.Wrap(errcode.Internal, "iterate workspace memory FTS rows", err)
	}
	return nil
}

func buildFTSQuery(tableName, workspaceID string, kind Kind, matchQuery string) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT e.id, e.workspace_id, e.kind, e.label, e.summary, e.body,
		e.keywords_json, e.anchors_json, e.author, e.verified,
		e.validity, e.invalid_reason, e.superseded_by, e.provenance_json,
		e.revision, e.updated_at,
		bm25(`)
	b.WriteString(tableName)
	b.WriteString(`, 10.0, 10.0, 4.0, 1.0)
		FROM `)
	b.WriteString(tableName)
	b.WriteString(` JOIN workspace_memory_entries e ON e.fts_rowid = `)
	b.WriteString(tableName)
	b.WriteString(`.rowid WHERE e.workspace_id = ? AND e.validity = 'valid' AND `)
	b.WriteString(tableName)
	b.WriteString(` MATCH ?`)
	args := []any{workspaceID, matchQuery}
	if kind != "" {
		b.WriteString(" AND e.kind = ?")
		args = append(args, string(kind))
	}
	return b.String(), args
}

func scanFTSRow(sc rowScanner) (row entryRow, bm25Val float64, err error) {
	var (
		kind, author, validity    string
		kwJSON, ancJSON, provJSON string
		verifiedInt               int
	)
	scanErr := sc.Scan(
		&row.id, &row.workspaceID, &kind, &row.label, &row.summary, &row.body,
		&kwJSON, &ancJSON, &author, &verifiedInt,
		&validity, &row.invalidReason, &row.supersededBy, &provJSON,
		&row.revision, &row.updatedAtMs,
		&bm25Val,
	)
	if scanErr != nil {
		return entryRow{}, 0, errcode.Wrap(errcode.Internal, "scan workspace memory FTS row", scanErr)
	}
	row.kind = Kind(kind)
	row.author = Author(author)
	row.validity = Validity(validity)
	row.verified = verifiedInt != 0
	if err := unmarshalEntryJSON(kwJSON, ancJSON, provJSON, &row.keywords, &row.anchors, &row.provenance); err != nil {
		return entryRow{}, 0, err
	}
	return row, bm25Val, nil
}

func recordFTSCandidate(out map[string]*rawSearchCandidate, row *entryRow, score float64, isWord bool) {
	cand, ok := out[row.id]
	if !ok {
		cand = &rawSearchCandidate{row: *row}
		out[row.id] = cand
	}
	if isWord {
		cand.wordScore = max(cand.wordScore, score)
	} else {
		cand.triScore = max(cand.triScore, score)
	}
}

func (s *Store) collectInvalidMatches(ctx context.Context, in *SearchInput, out map[string]*rawSearchCandidate) error {
	tokens := extractWordTokens(in.Query)
	if len(tokens) == 0 {
		return nil
	}
	rows, err := s.readInvalidRows(ctx, in.WorkspaceID, in.Kind)
	if err != nil {
		return err
	}
	for i := range rows {
		score := matchInvalidRow(&rows[i], tokens)
		if score <= 0 {
			continue
		}
		if _, exists := out[rows[i].id]; !exists {
			out[rows[i].id] = &rawSearchCandidate{
				row:      rows[i],
				invScore: score,
			}
		}
	}
	return nil
}

func buildMatchSnippet(row *entryRow, rawQuery string) string {
	needles := snippetNeedles(rawQuery)
	fields := []string{row.summary, row.body, row.label, strings.Join(row.keywords, ", ")}
	for _, needle := range needles {
		for _, field := range fields {
			if field == "" {
				continue
			}
			if snip, ok := extractSnippetWindow(field, needle); ok {
				return snip
			}
		}
	}
	return fallbackSnippet(row.summary, row.body)
}

func snippetNeedles(rawQuery string) []string {
	var needles []string
	if trimmed := strings.TrimSpace(rawQuery); trimmed != "" {
		needles = append(needles, trimmed)
	}
	needles = append(needles, extractTrigramTokens(rawQuery)...)
	needles = append(needles, extractWordTokens(rawQuery)...)
	return needles
}

func extractSnippetWindow(text, needle string) (string, bool) {
	lowText := strings.ToLower(text)
	lowNeedle := strings.ToLower(needle)
	byteIdx := strings.Index(lowText, lowNeedle)
	if byteIdx < 0 {
		return "", false
	}
	runeStart := utf8.RuneCountInString(lowText[:byteIdx])
	runes := []rune(text)
	if len(runes) <= maxSnippetRunes {
		return text, true
	}
	half := maxSnippetRunes / 2
	start := max(0, runeStart-half)
	end := min(len(runes), start+maxSnippetRunes)
	if end == len(runes) {
		start = max(0, end-maxSnippetRunes)
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "..." + out
	}
	if end < len(runes) {
		out += "..."
	}
	return out, true
}

func (s *Store) readInvalidRows(ctx context.Context, workspaceID string, kind Kind) ([]entryRow, error) {
	query := `SELECT id, workspace_id, kind, label, summary, body,
		keywords_json, anchors_json, author, verified,
		validity, invalid_reason, superseded_by, provenance_json,
		revision, updated_at
		FROM workspace_memory_entries WHERE workspace_id = ? AND validity != 'valid'`
	args := []any{workspaceID}
	if kind != "" {
		query += " AND kind = ?"
		args = append(args, string(kind))
	}
	return s.readListRows(ctx, query, args)
}

func matchInvalidRow(row *entryRow, tokens []string) float64 {
	labelLow := strings.ToLower(row.label)
	kwLow := strings.ToLower(strings.Join(row.keywords, " "))
	sumLow := strings.ToLower(row.summary)
	bodyLow := strings.ToLower(row.body)
	var hits float64
	for _, tok := range tokens {
		low := strings.ToLower(tok)
		switch {
		case strings.Contains(labelLow, low), strings.Contains(kwLow, low):
			hits += 10
		case strings.Contains(sumLow, low):
			hits += 4
		case strings.Contains(bodyLow, low):
			hits += 1
		default:
			return 0
		}
	}
	return hits / (hits + 10)
}

func sanitizeWordQuery(raw string) string {
	tokens := extractWordTokens(raw)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		quoted = append(quoted, `"`+tok+`"*`)
	}
	return strings.Join(quoted, " ")
}

func extractWordTokens(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			tokens = append(tokens, p)
		}
	}
	return tokens
}

func sanitizeTrigramQuery(raw string) string {
	tokens := extractTrigramTokens(raw)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		quoted = append(quoted, `"`+tok+`"`)
	}
	return strings.Join(quoted, " ")
}

func extractTrigramTokens(raw string) []string {
	fields := strings.Fields(raw)
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		cleaned := stripFTSOperators(f)
		if utf8.RuneCountInString(cleaned) >= 3 {
			tokens = append(tokens, cleaned)
		}
	}
	return tokens
}

func stripFTSOperators(tok string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '(', ')', '*', '^', ':':
			return -1
		default:
			return r
		}
	}, tok)
}

func buildApproxTrigramQuery(raw string) string {
	tokens := extractTrigramTokens(raw)
	if len(tokens) == 0 {
		return ""
	}
	seen := map[string]bool{}
	grams := make([]string, 0, maxApproxNgrams)
	for _, tok := range tokens {
		runes := []rune(tok)
		for i := 0; i+3 <= len(runes); i++ {
			g := string(runes[i : i+3])
			if seen[g] {
				continue
			}
			seen[g] = true
			grams = append(grams, `"`+g+`"`)
			if len(grams) >= maxApproxNgrams {
				return strings.Join(grams, " OR ")
			}
		}
	}
	return strings.Join(grams, " OR ")
}
