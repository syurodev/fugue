package workspacememory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/storage"
)

const (
	testWSID         = "ws-1"
	testAuthLabel    = "auth-architecture"
	testAppPrincipal = "app"
	testUniqueLabel  = "unique-label"
	testRateSession  = "sess-rate"
	testMainRoot     = "main"
)

type fakeSecretScanner struct {
	tokens map[string]string
}

func (f *fakeSecretScanner) Scan(text string) []SecretFinding {
	var out []SecretFinding
	for tok, kind := range f.tokens {
		if strings.Contains(text, tok) {
			out = append(out, SecretFinding{Kind: kind, Key: ""})
		}
	}
	return out
}

type fakeAnchorHasher struct {
	mu    sync.Mutex
	files map[string]map[string][]byte // rootName -> relPath -> bytes
}

func newFakeAnchorHasher() *fakeAnchorHasher {
	return &fakeAnchorHasher{files: map[string]map[string][]byte{}}
}

func (r *fakeAnchorHasher) setFile(root, rel string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.files[root] == nil {
		r.files[root] = map[string][]byte{}
	}
	r.files[root][rel] = append([]byte(nil), data...)
}

func (r *fakeAnchorHasher) deleteFile(root, rel string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.files[root], rel)
}

func (r *fakeAnchorHasher) deleteRoot(root string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.files, root)
}

func (r *fakeAnchorHasher) HashAnchor(
	_ context.Context,
	_ string,
	rootName, relPath string,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, ok := r.files[rootName]
	if !ok {
		return "", errcode.New(errcode.WorkspaceRootMissing, "root missing")
	}
	data, ok := root[relPath]
	if !ok {
		return "", errcode.New(errcode.WorkspaceFileNotFound, "file not found")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type fakeEventSink struct {
	mu     sync.Mutex
	events []WrittenEvent
}

func (s *fakeEventSink) OnMemoryEntryWritten(_ context.Context, ev WrittenEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *fakeEventSink) snapshot() []WrittenEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WrittenEvent, len(s.events))
	copy(out, s.events)
	return out
}

type testEnv struct {
	db      *sql.DB
	clk     *clock.Fake
	ids     ids.Generator
	scanner *fakeSecretScanner
	anchors *fakeAnchorHasher
	events  *fakeEventSink
	store   *Store
}

func newTestEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("db.Close: %v", cerr)
		}
	})
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("storage.Migrate: %v", migErr)
	}
	gen := ids.NewSequence("mem")
	scanner := &fakeSecretScanner{
		tokens: map[string]string{
			"gh" + "p_" + strings.Repeat("a1", 18): "github-token",
			"AK" + "IA" + strings.Repeat("A1", 8):  "aws-access-key",
		},
	}
	anchors := newFakeAnchorHasher()
	events := &fakeEventSink{}
	store, err := NewStore(db, clk, gen, scanner, anchors, events, cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &testEnv{
		db:      db,
		clk:     clk,
		ids:     gen,
		scanner: scanner,
		anchors: anchors,
		events:  events,
		store:   store,
	}
}

func TestWME01WritesCASHistoryNoDelete(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	created := mustAddAuthEntry(t, ctx, env)
	env.clk.Advance(time.Minute)
	updated := mustUpdateAuthEntry(t, ctx, env, created.ID)
	assertCASConflictReturnsCurrent(t, ctx, env, created.ID, &updated)
	assertInvalidateAndHistory(t, ctx, env, created.ID)
}

func mustAddAuthEntry(t *testing.T, ctx context.Context, env *testEnv) Entry {
	t.Helper()
	created, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindDecision,
		Label:       testAuthLabel,
		Summary:     "The host enforces authorization in Go services before dispatch.",
		Body:        "The host validates permissions in Go services because clients cannot be trusted.",
		Keywords:    []string{"auth", "host"},
		Author:      AuthorAgent,
		Provenance:  Provenance{Principal: "session", SessionID: "sess-1", Role: "lead"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if created.Revision != 1 || created.Validity != ValidityValid {
		t.Fatalf("Add got revision=%d validity=%q, want 1 valid", created.Revision, created.Validity)
	}
	return created
}

func mustUpdateAuthEntry(t *testing.T, ctx context.Context, env *testEnv, id string) Entry {
	t.Helper()
	updated, err := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               id,
		ExpectedRevision: 1,
		Kind:             KindDecision,
		Label:            testAuthLabel,
		Summary:          "The host enforces authorization in Go services on every request.",
		Body:             "The host validates permissions in Go services on every request before running tools.",
		Keywords:         []string{"auth", "go"},
		Author:           AuthorUser,
		Provenance:       Provenance{Principal: testAppPrincipal},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("Update revision = %d, want 2", updated.Revision)
	}
	return updated
}

func assertCASConflictReturnsCurrent(t *testing.T, ctx context.Context, env *testEnv, id string, want *Entry) {
	t.Helper()
	_, conflictErr := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               id,
		ExpectedRevision: 1,
		Kind:             KindDecision,
		Label:            testAuthLabel,
		Summary:          "This stale update should fail with revision conflict.",
		Body:             "This stale update should fail because revision 1 is no longer current.",
		Author:           AuthorAgent,
	})
	if errcode.CodeOf(conflictErr) != errcode.MemoryRevisionConflict {
		t.Fatalf("stale Update code = %q, want %q (err=%v)", errcode.CodeOf(conflictErr), errcode.MemoryRevisionConflict, conflictErr)
	}
	var revErr *RevisionConflictError
	if !errors.As(conflictErr, &revErr) {
		t.Fatalf("stale Update error %T does not wrap *RevisionConflictError", conflictErr)
	}
	if diff := cmp.Diff(*want, revErr.CurrentEntry()); diff != "" {
		t.Fatalf("RevisionConflictError.CurrentEntry diff (-want +got):\n%s", diff)
	}
	_, staleInvErr := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               id,
		ExpectedRevision: 1,
		Reason:           "Replaced by a newer decision.",
		Author:           AuthorUser,
	})
	if errcode.CodeOf(staleInvErr) != errcode.MemoryRevisionConflict {
		t.Fatalf("stale Invalidate code = %q, want %q", errcode.CodeOf(staleInvErr), errcode.MemoryRevisionConflict)
	}
}

func assertInvalidateAndHistory(t *testing.T, ctx context.Context, env *testEnv, id string) {
	t.Helper()
	replacement, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindDecision,
		Label:       "auth-v2",
		Summary:     "New authorization decision replaces the previous version.",
		Body:        "This decision supersedes the older entry while keeping full audit history.",
		Author:      AuthorUser,
	})
	if err != nil {
		t.Fatalf("Add replacement: %v", err)
	}
	env.clk.Advance(time.Minute)
	inv, err := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               id,
		ExpectedRevision: 2,
		Reason:           "Superseded by auth-v2.",
		SupersededBy:     replacement.ID,
		Author:           AuthorUser,
	})
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if inv.Revision != 3 || inv.Validity != ValiditySuperseded || inv.SupersededBy != replacement.ID {
		t.Fatalf("Invalidate got rev=%d validity=%q supersededBy=%q", inv.Revision, inv.Validity, inv.SupersededBy)
	}
	fetched, err := env.store.GetByID(ctx, testWSID, id)
	if err != nil {
		t.Fatalf("GetByID after invalidate: %v", err)
	}
	if fetched.Validity != ValiditySuperseded {
		t.Fatalf("GetByID validity = %q, want superseded", fetched.Validity)
	}
	if _, labelErr := env.store.GetByLabel(ctx, testWSID, testAuthLabel); errcode.CodeOf(labelErr) != errcode.NotFound {
		t.Fatalf("GetByLabel on superseded code = %q, want not_found", errcode.CodeOf(labelErr))
	}
	assertHistoryOps(t, ctx, env, id, []Operation{OpAdd, OpUpdate, OpInvalidate})
}

func assertHistoryOps(t *testing.T, ctx context.Context, env *testEnv, id string, want []Operation) {
	t.Helper()
	hist, err := env.store.History(ctx, testWSID, id)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != len(want) {
		t.Fatalf("History len = %d, want %d", len(hist), len(want))
	}
	for i, w := range want {
		if hist[i].Op != w {
			t.Fatalf("History[%d].Op = %q, want %q", i, hist[i].Op, w)
		}
	}
}

func TestWME02VerifiedRules(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	userEntry, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "user-fact",
		Summary:     "The repository runs targeted tests through task commands.",
		Body:        "Developers use task test-pkg to run package tests in the workspace.",
		Author:      AuthorUser,
	})
	if err != nil {
		t.Fatalf("user Add: %v", err)
	}
	if !userEntry.Verified {
		t.Fatal("user Add verified = false, want true")
	}

	agentEntry, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindNote,
		Label:       "agent-note",
		Summary:     "The build pipeline uses Taskfile targets for checks.",
		Body:        "The build pipeline defines targeted test and lint tasks in Taskfile.yml.",
		Author:      AuthorAgent,
	})
	if err != nil {
		t.Fatalf("agent Add: %v", err)
	}
	if agentEntry.Verified {
		t.Fatal("agent Add verified = true, want false")
	}

	assertVerifyDemoteAndRestore(t, ctx, env, agentEntry.ID)
}

func assertVerifyDemoteAndRestore(t *testing.T, ctx context.Context, env *testEnv, entryID string) {
	t.Helper()
	assertVerifyCASAndPromote(t, ctx, env, entryID)
	demoted, err := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               entryID,
		ExpectedRevision: 2,
		Kind:             KindNote,
		Label:            "agent-note",
		Summary:          "The build pipeline uses Taskfile targets and arch checks.",
		Body:             "The build pipeline runs Taskfile targets and architecture checks on every PR.",
		Author:           AuthorAgent,
	})
	if err != nil {
		t.Fatalf("agent Update: %v", err)
	}
	if demoted.Verified || demoted.Revision != 3 || demoted.Author != AuthorAgent {
		t.Fatalf("agent Update got verified=%v rev=%d author=%q", demoted.Verified, demoted.Revision, demoted.Author)
	}
	restored, err := env.store.Restore(ctx, &RestoreInput{
		WorkspaceID:      testWSID,
		ID:               entryID,
		RestoreRevision:  1,
		ExpectedRevision: 3,
		Provenance:       Provenance{Principal: testAppPrincipal},
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !restored.Verified || restored.Author != AuthorUser || restored.Revision != 4 {
		t.Fatalf("Restore got verified=%v author=%q rev=%d", restored.Verified, restored.Author, restored.Revision)
	}
}

func assertVerifyCASAndPromote(t *testing.T, ctx context.Context, env *testEnv, entryID string) {
	t.Helper()
	if _, vErr := env.store.Verify(ctx, &VerifyInput{
		WorkspaceID:      testWSID,
		ID:               entryID,
		ExpectedRevision: 99,
	}); errcode.CodeOf(vErr) != errcode.MemoryRevisionConflict {
		t.Fatalf("stale Verify code = %q, want %q", errcode.CodeOf(vErr), errcode.MemoryRevisionConflict)
	}
	verified, err := env.store.Verify(ctx, &VerifyInput{
		WorkspaceID:      testWSID,
		ID:               entryID,
		ExpectedRevision: 1,
		Provenance:       Provenance{Principal: testAppPrincipal},
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !verified.Verified || verified.Revision != 2 || verified.Author != AuthorUser {
		t.Fatalf("Verify got verified=%v rev=%d author=%q", verified.Verified, verified.Revision, verified.Author)
	}
}

func TestWME05WriteChecksAndNoSecretLeak(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	var logBuf bytes.Buffer
	logger := slog.New(redact.NewTextHandler(&logBuf, slog.LevelDebug))
	env := newTestEnv(t, Config{
		MaxWritesPerSessionHour:     3,
		MaxValidEntriesPerWorkspace: 4,
		Logger:                      logger,
	})

	assertSecretRejection(t, ctx, env, &logBuf)
	assertSizeAndLanguageChecks(t, ctx, env)
	assertRateAndCapacityLimits(t, ctx, env)
}

func assertSecretRejection(t *testing.T, ctx context.Context, env *testEnv, logBuf *bytes.Buffer) {
	t.Helper()
	fakeGH := "gh" + "p_" + strings.Repeat("a1", 18)
	fakeAWS := "AK" + "IA" + strings.Repeat("A1", 8)

	_, secErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindPinned,
		Label:       "secret-test",
		Summary:     strings.Repeat("word ", 60),
		Body:        "Here is a token " + fakeGH + " embedded in oversized non-English text: Xin chào đây là tiếng Việt rất dài.",
		Author:      AuthorAgent,
	})
	if got := errcode.CodeOf(secErr); got != errcode.MemoryContainsSecret {
		t.Fatalf("secret Add code = %q, want memory.contains_secret", got)
	}
	if strings.Contains(secErr.Error(), fakeGH) || strings.Contains(logBuf.String(), fakeGH) {
		t.Fatal("secret value leaked into error or logs")
	}

	_, kwSecErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "kw-secret",
		Summary:     "Valid summary for checking keyword secret scanning.",
		Body:        "This body explains the workspace build rules in clear English sentences.",
		Keywords:    []string{fakeAWS},
		Author:      AuthorUser,
	})
	if got := errcode.CodeOf(kwSecErr); got != errcode.MemoryContainsSecret {
		t.Fatalf("keyword secret code = %q, want memory.contains_secret", got)
	}
	if strings.Contains(kwSecErr.Error(), fakeAWS) || strings.Contains(logBuf.String(), fakeAWS) {
		t.Fatal("AWS secret value leaked into error or logs")
	}
	if len(env.events.snapshot()) != 0 {
		t.Fatalf("rejected writes emitted %d events, want 0", len(env.events.snapshot()))
	}
}

func assertSizeAndLanguageChecks(t *testing.T, ctx context.Context, env *testEnv) {
	t.Helper()
	_, bigSummaryErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "big-summary",
		Summary:     strings.Repeat("a", 201),
		Body:        "This body is valid English prose describing the layout.",
		Author:      AuthorUser,
	})
	if errcode.CodeOf(bigSummaryErr) != errcode.MemoryTooLarge {
		t.Fatalf("oversize summary code = %q, want %q", errcode.CodeOf(bigSummaryErr), errcode.MemoryTooLarge)
	}
	_, bigPinnedErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindPinned,
		Label:       "big-pinned",
		Summary:     "Pinned block summary.",
		Body:        strings.Repeat("word ", 500),
		Author:      AuthorUser,
	})
	if errcode.CodeOf(bigPinnedErr) != errcode.MemoryTooLarge {
		t.Fatalf("oversize pinned code = %q, want %q", errcode.CodeOf(bigPinnedErr), errcode.MemoryTooLarge)
	}
	_, langErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindConvention,
		Label:       "vn-only",
		Summary:     "Quy ước đặt tên và cấu trúc thư mục cho toàn bộ dự án mới.",
		Body:        "Mỗi dịch vụ phải kiểm tra quyền ở tầng máy chủ trước khi thực thi bất kỳ lệnh nào trong phiên làm việc.",
		Author:      AuthorAgent,
	})
	if errcode.CodeOf(langErr) != errcode.MemoryLanguage {
		t.Fatalf("language Add code = %q, want %q", errcode.CodeOf(langErr), errcode.MemoryLanguage)
	}
}

func assertRateAndCapacityLimits(t *testing.T, ctx context.Context, env *testEnv) {
	t.Helper()
	first, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       testUniqueLabel,
		Summary:     "First valid entry with this label.",
		Body:        "This entry describes the repository layout and test configuration.",
		Author:      AuthorUser,
		Provenance:  Provenance{SessionID: testRateSession},
	})
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	_, dupErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       testUniqueLabel,
		Summary:     "Duplicate valid entry with the same label.",
		Body:        "This entry should be rejected because another valid entry holds the label.",
		Author:      AuthorUser,
	})
	if errcode.CodeOf(dupErr) != errcode.MemoryLabelTaken {
		t.Fatalf("duplicate label code = %q, want %q", errcode.CodeOf(dupErr), errcode.MemoryLabelTaken)
	}
	fillRateAndCapacityWindow(t, ctx, env)
	assertInvalidateFreesSlotAndEvents(t, ctx, env, &first)
}

func fillRateAndCapacityWindow(t *testing.T, ctx context.Context, env *testEnv) {
	t.Helper()
	for _, lbl := range []string{"rate-2", "rate-3"} {
		if _, addErr := env.store.Add(ctx, &AddInput{
			WorkspaceID: testWSID,
			Kind:        KindFact,
			Label:       lbl,
			Summary:     "Entry written inside the session rate window.",
			Body:        "This entry counts toward the per-session hourly write budget.",
			Author:      AuthorAgent,
			Provenance:  Provenance{SessionID: testRateSession},
		}); addErr != nil {
			t.Fatalf("Add %s: %v", lbl, addErr)
		}
	}
	_, rateErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "rate-4",
		Summary:     "Fourth entry in the same hour should fail rate limit.",
		Body:        "This write exceeds three writes within one hour for the session.",
		Author:      AuthorAgent,
		Provenance:  Provenance{SessionID: testRateSession},
	})
	if errcode.CodeOf(rateErr) != errcode.MemoryRateLimited {
		t.Fatalf("rate-limited Add code = %q, want %q", errcode.CodeOf(rateErr), errcode.MemoryRateLimited)
	}
	env.clk.Advance(61 * time.Minute)
	if _, addErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "cap-4",
		Summary:     "Fourth valid entry after rate window expires.",
		Body:        "This entry fills the workspace valid entry capacity of four.",
		Author:      AuthorAgent,
		Provenance:  Provenance{SessionID: testRateSession},
	}); addErr != nil {
		t.Fatalf("Add cap-4 after clock advance: %v", addErr)
	}
	_, capErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "cap-5",
		Summary:     "Fifth valid entry exceeds workspace capacity.",
		Body:        "This write should fail because four valid entries already exist.",
		Author:      AuthorUser,
	})
	if errcode.CodeOf(capErr) != errcode.MemoryLimitReached {
		t.Fatalf("workspace limit Add code = %q, want %q", errcode.CodeOf(capErr), errcode.MemoryLimitReached)
	}
}

func assertInvalidateFreesSlotAndEvents(t *testing.T, ctx context.Context, env *testEnv, first *Entry) {
	t.Helper()
	if _, invErr := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               first.ID,
		ExpectedRevision: first.Revision,
		Reason:           "No longer needed.",
		Author:           AuthorUser,
	}); invErr != nil {
		t.Fatalf("Invalidate first: %v", invErr)
	}
	if _, reuseErr := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       testUniqueLabel,
		Summary:     "Label reused after invalidation.",
		Body:        "This entry reuses the freed label and valid capacity slot.",
		Author:      AuthorUser,
	}); reuseErr != nil {
		t.Fatalf("Add reusing label after invalidate: %v", reuseErr)
	}
	evs := env.events.snapshot()
	if len(evs) == 0 {
		t.Fatal("expected emitted MemoryEntryWritten events")
	}
	for _, ev := range evs {
		if ev.EntryID == "" || ev.Revision == 0 {
			t.Fatalf("invalid emitted event: %+v", ev)
		}
	}
}

func TestWME07Freshness(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	const rel = "docs/arch.md"
	env.anchors.setFile(testMainRoot, rel, []byte("version 1 of the architecture document"))

	noAnchor, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindNote,
		Label:       "no-anchor",
		Summary:     "Entry without anchors reports unknown freshness.",
		Body:        "This note has no file anchors so its freshness is unknown.",
		Author:      AuthorUser,
	})
	if err != nil {
		t.Fatalf("Add noAnchor: %v", err)
	}
	if noAnchor.Freshness != FreshnessUnknown {
		t.Fatalf("noAnchor freshness = %q, want unknown", noAnchor.Freshness)
	}

	anchored, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "with-anchor",
		Summary:     "Entry anchored to docs/arch.md reports fresh initially.",
		Body:        "This fact anchors docs/arch.md and tracks content hash changes.",
		Anchors:     []AnchorInput{{RootName: testMainRoot, RelPath: rel}},
		Author:      AuthorAgent,
	})
	if err != nil {
		t.Fatalf("Add anchored: %v", err)
	}
	if anchored.Freshness != FreshnessFresh || len(anchored.Anchors) != 1 || anchored.Anchors[0].ContentHash == "" {
		t.Fatalf("anchored initial state = %+v", anchored)
	}

	assertFreshnessTransitions(t, ctx, env, anchored.ID, rel)
}

func assertFreshnessTransitions(t *testing.T, ctx context.Context, env *testEnv, entryID, rel string) {
	t.Helper()
	env.anchors.setFile(testMainRoot, rel, []byte("version 2 of the architecture document"))
	gotByID := mustGetByID(t, ctx, env, entryID)
	if gotByID.Freshness != FreshnessStale || gotByID.Author != AuthorAgent || gotByID.Verified || gotByID.Revision != 1 {
		t.Fatalf("GetByID after edit = freshness:%q author:%q verified:%v rev:%d", gotByID.Freshness, gotByID.Author, gotByID.Verified, gotByID.Revision)
	}
	gotByLabel, err := env.store.GetByLabel(ctx, testWSID, "with-anchor")
	if err != nil {
		t.Fatalf("GetByLabel after edit: %v", err)
	}
	if gotByLabel.Freshness != FreshnessStale {
		t.Fatalf("GetByLabel freshness = %q, want stale", gotByLabel.Freshness)
	}

	env.anchors.setFile(testMainRoot, rel, []byte("version 1 of the architecture document"))
	listed, err := env.store.List(ctx, ListFilter{WorkspaceID: testWSID, Kind: KindFact})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Freshness != FreshnessFresh {
		t.Fatalf("List after restore = %+v", listed)
	}

	env.anchors.deleteFile(testMainRoot, rel)
	if got := mustGetByID(t, ctx, env, entryID); got.Freshness != FreshnessStale {
		t.Fatalf("GetByID with missing file freshness = %q, want stale", got.Freshness)
	}

	env.anchors.deleteRoot(testMainRoot)
	if got := mustGetByID(t, ctx, env, entryID); got.Freshness != FreshnessStale {
		t.Fatalf("GetByID with missing root freshness = %q, want stale", got.Freshness)
	}
}

func mustGetByID(t *testing.T, ctx context.Context, env *testEnv, entryID string) Entry {
	t.Helper()
	got, err := env.store.GetByID(ctx, testWSID, entryID)
	if err != nil {
		t.Fatalf("GetByID %s: %v", entryID, err)
	}
	return got
}
