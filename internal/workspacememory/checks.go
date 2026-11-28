package workspacememory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

type writeCandidate struct {
	workspaceID   string
	entryID       string
	kind          Kind
	label         string
	summary       string
	body          string
	keywords      []string
	invalidReason string
	author        Author
	sessionID     string
	checkContent  bool
}

func (s *Store) runPureChecks(cand *writeCandidate) error {
	// Order per workspace-memory.mdx §3:
	// 1. Secret scan (reject before size/language/uniqueness without leaking).
	if err := s.checkSecrets(cand); err != nil {
		return err
	}
	if cand.checkContent {
		// 2. Size limits (§2).
		if err := validateEntrySizes(cand); err != nil {
			return err
		}
		// 3. C-08 language check.
		if err := CheckLanguage(cand.summary+"\n"+cand.body, s.cfg.LanguageMinWords, s.cfg.LanguageMaxNonEnglishRatio); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) runDBChecks(ctx context.Context, tx *sql.Tx, cand *writeCandidate) error {
	if cand.checkContent {
		// 4. Label unique among valid entries in the same workspace.
		if err := ensureLabelAvailable(ctx, tx, cand.workspaceID, cand.label, cand.entryID); err != nil {
			return err
		}
	}
	// 5. Per-session rate limit and per-workspace valid entry capacity.
	return s.checkLimits(ctx, tx, cand)
}

func (s *Store) runWriteChecks(ctx context.Context, tx *sql.Tx, cand *writeCandidate) error {
	if err := s.runPureChecks(cand); err != nil {
		return err
	}
	return s.runDBChecks(ctx, tx, cand)
}

func (s *Store) checkSecrets(cand *writeCandidate) error {
	fields := make([]string, 0, 4+len(cand.keywords))
	fields = append(fields, cand.label, cand.summary, cand.body, cand.invalidReason)
	fields = append(fields, cand.keywords...)
	for _, field := range fields {
		if field == "" {
			continue
		}
		if findings := s.scanner.Scan(field); len(findings) > 0 {
			return errContainsSecret(findings[0].Kind)
		}
	}
	return nil
}

func validateEntrySizes(cand *writeCandidate) error {
	if !validKind(cand.kind) {
		return errInvalidArg("invalid workspace memory kind")
	}
	if !validAuthor(cand.author) {
		return errInvalidArg("invalid workspace memory author")
	}
	if strings.TrimSpace(cand.label) == "" {
		return errInvalidArg("memory label is required")
	}
	if strings.ContainsAny(cand.label, "\r\n") {
		return errInvalidArg("memory label must be a single line")
	}
	if utf8.RuneCountInString(cand.label) > MaxLabelRunes {
		return errTooLarge("memory label exceeds 128 characters")
	}
	if err := validateSummaryAndBody(cand.kind, cand.summary, cand.body); err != nil {
		return err
	}
	return validateKeywords(cand.keywords)
}

func validateSummaryAndBody(kind Kind, summary, body string) error {
	if strings.TrimSpace(summary) == "" {
		return errInvalidArg("memory summary is required")
	}
	if strings.ContainsAny(summary, "\r\n") {
		return errInvalidArg("memory summary must be a single line")
	}
	if utf8.RuneCountInString(summary) > MaxSummaryRunes {
		return errTooLarge("memory summary exceeds 200 characters")
	}
	maxBody := MaxBodyBytes
	if kind == KindPinned {
		maxBody = MaxPinnedBodyBytes
	}
	if len(body) > maxBody {
		return errTooLarge("memory body exceeds size limit")
	}
	return nil
}

func validateKeywords(keywords []string) error {
	if len(keywords) > MaxKeywords {
		return errTooLarge("memory keywords exceed 20 entries")
	}
	for _, kw := range keywords {
		if strings.TrimSpace(kw) == "" {
			return errInvalidArg("memory keyword must not be empty")
		}
		if utf8.RuneCountInString(kw) > MaxKeywordRunes {
			return errTooLarge("memory keyword exceeds 40 characters")
		}
	}
	return nil
}

func ensureLabelAvailable(ctx context.Context, tx *sql.Tx, workspaceID, label, selfID string) error {
	var existingID string
	err := tx.QueryRowContext(
		ctx,
		`SELECT id FROM workspace_memory_entries WHERE workspace_id = ? AND label = ? AND validity = 'valid' LIMIT 1`,
		workspaceID, label,
	).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errcode.Wrap(errcode.Internal, "check label uniqueness", err)
	}
	if existingID != selfID {
		return errLabelTaken()
	}
	return nil
}

func (s *Store) checkLimits(ctx context.Context, tx *sql.Tx, cand *writeCandidate) error {
	if cand.sessionID != "" {
		windowStart := s.clk.Now().UTC().Add(-time.Hour).UnixMilli()
		var writes int
		err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM workspace_memory_history WHERE session_id = ? AND written_at >= ?`,
			cand.sessionID, windowStart,
		).Scan(&writes)
		if err != nil {
			return errcode.Wrap(errcode.Internal, "count session memory writes", err)
		}
		if writes >= s.cfg.MaxWritesPerSessionHour {
			return errRateLimited()
		}
	}
	if cand.checkContent {
		if err := s.checkWorkspaceCapacity(ctx, tx, cand.workspaceID, cand.entryID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) checkWorkspaceCapacity(ctx context.Context, tx *sql.Tx, workspaceID, selfID string) error {
	if selfID != "" {
		var currentValidity string
		err := tx.QueryRowContext(
			ctx,
			`SELECT validity FROM workspace_memory_entries WHERE workspace_id = ? AND id = ?`,
			workspaceID, selfID,
		).Scan(&currentValidity)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return errcode.Wrap(errcode.Internal, "check existing entry validity", err)
		}
		if currentValidity == string(ValidityValid) {
			return nil
		}
	}
	var count int
	err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM workspace_memory_entries WHERE workspace_id = ? AND validity = 'valid'`,
		workspaceID,
	).Scan(&count)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "count valid workspace memory entries", err)
	}
	if count >= s.cfg.MaxValidEntriesPerWorkspace {
		return errLimitReached()
	}
	return nil
}
