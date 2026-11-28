package workspacememory

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
)

const (
	testKindGitHubToken = "github-token"
	testKindAWSKey      = "aws-access-key"
)

func TestExportRefusesPlantedSecretInEveryField(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var logBuf bytes.Buffer
	logger := slog.New(redact.NewTextHandler(&logBuf, slog.LevelDebug))
	env := newTestEnv(t, Config{Logger: logger})

	tokLabel := "gh" + "p_" + strings.Repeat("b1", 18)
	tokSummary := "AK" + "IA" + strings.Repeat("B2", 8)
	tokKeyword := "gh" + "p_" + strings.Repeat("c3", 18)
	tokReason := "AK" + "IA" + strings.Repeat("D4", 8)
	env.scanner.tokens[tokLabel] = testKindGitHubToken
	env.scanner.tokens[tokSummary] = testKindAWSKey
	env.scanner.tokens[tokKeyword] = testKindGitHubToken
	env.scanner.tokens[tokReason] = testKindAWSKey

	idLabel := plantLegacyEntry(t, ctx, env, "clean-for-label", `UPDATE workspace_memory_entries SET label = ? WHERE id = ?`, "prefix "+tokLabel)
	idSummary := plantLegacyEntry(t, ctx, env, "clean-summary-lbl", `UPDATE workspace_memory_entries SET summary = ? WHERE id = ?`, "summary "+tokSummary)
	kwJSON, mErr := json.Marshal([]string{tokKeyword})
	if mErr != nil {
		t.Fatalf("marshal keywords: %v", mErr)
	}
	idKW := plantLegacyEntry(t, ctx, env, "clean-kw-lbl", `UPDATE workspace_memory_entries SET keywords_json = ? WHERE id = ?`, string(kwJSON))
	idReason := plantLegacyEntry(t, ctx, env, "clean-reason-lbl", `UPDATE workspace_memory_entries SET validity = 'invalid', invalid_reason = ? WHERE id = ?`, "reason "+tokReason)

	_, expErr := env.store.ExportMarkdown(ctx, testWSID, "ws-every-field")
	if errcode.CodeOf(expErr) != errcode.MemoryContainsSecret {
		t.Fatalf("ExportMarkdown code = %q, want memory.contains_secret", errcode.CodeOf(expErr))
	}
	msg := expErr.Error()
	for _, want := range []string{idLabel, idSummary, idKW, idReason, testKindGitHubToken, testKindAWSKey, "clean-summary-lbl", "clean-kw-lbl", "clean-reason-lbl"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("ExportMarkdown error missing %q in %q", want, msg)
		}
	}
	for _, secret := range []string{tokLabel, tokSummary, tokKeyword, tokReason} {
		if strings.Contains(msg, secret) || strings.Contains(logBuf.String(), secret) {
			t.Fatal("planted secret value leaked into export error or logs")
		}
	}
}

func plantLegacyEntry(t *testing.T, ctx context.Context, env *testEnv, label, updateSQL, value string) string {
	t.Helper()
	e, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindFact, Label: label,
		Summary: "Clean summary before legacy mutation.", Body: "This entry has clean English prose before planting a value.",
		Author: AuthorUser,
	})
	if err != nil {
		t.Fatalf("Add %s: %v", label, err)
	}
	if _, execErr := env.db.ExecContext(ctx, updateSQL, value, e.ID); execErr != nil {
		t.Fatalf("update %s: %v", label, execErr)
	}
	return e.ID
}

func TestPreviewSecretRejectionOmitsContent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dst := newTestEnv(t, Config{})

	tokBody := "gh" + "p_" + strings.Repeat("e5", 18)
	tokLabel := "AK" + "IA" + strings.Repeat("F6", 8)
	tokRawLabel := "gh" + "p_" + strings.Repeat("a7", 18)
	dst.scanner.tokens[tokBody] = testKindGitHubToken
	dst.scanner.tokens[tokLabel] = testKindAWSKey
	dst.scanner.tokens[tokRawLabel] = testKindGitHubToken
	secBody := "Contains detected token " + tokBody + " in valid English prose."
	cleanBody := "Valid English prose without tokens inside the body text."
	oversizeSummary := strings.Repeat("x", MaxSummaryRunes+10)

	content := "---\nformat: fugue-memory/1\nworkspace: src\nexported_at: 2026-11-28T12:00:00Z\n---\n\n" +
		previewBlock("mem-sec-body", "clean-body-label", "Clean summary.", secBody) +
		previewBlock("mem-sec-label", "lbl-"+tokLabel, "Clean summary.", cleanBody) +
		"<!-- fugue:entry -->\n## malformed-" + tokRawLabel + "\nnot-yaml\n<!-- /fugue:entry -->\n\n" +
		previewBlock("mem-oversize", "oversize-clean", oversizeSummary, cleanBody)

	preview, err := dst.store.PreviewImport(ctx, testDstWSID, []byte(content))
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if len(preview.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(preview.Items))
	}
	assertPreviewSecretItem(t, &preview.Items[0], "mem-sec-body", "clean-body-label", testKindGitHubToken)
	assertPreviewSecretItem(t, &preview.Items[1], "mem-sec-label", "", testKindAWSKey)
	if preview.Items[2].Class != ImportClassRejected || preview.Items[2].Label != "" || preview.Items[2].Imported != nil {
		t.Fatalf("malformed secret-label item = %+v, want rejected with blank Label and nil Imported", preview.Items[2])
	}
	if preview.Items[3].Class != ImportClassRejected || preview.Items[3].Label != "oversize-clean" || preview.Items[3].Imported == nil {
		t.Fatalf("oversize item = %+v, want rejected with Label and non-nil Imported", preview.Items[3])
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("json.Marshal(preview): %v", err)
	}
	for _, secret := range []string{tokBody, tokLabel, tokRawLabel} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("secret leaked into ImportPreview JSON")
		}
	}
}

func assertPreviewSecretItem(t *testing.T, it *ImportPreviewItem, wantID, wantLabel, wantKind string) {
	t.Helper()
	if it.Class != ImportClassRejected || it.EntryID != wantID || it.Label != wantLabel || it.Imported != nil {
		t.Fatalf("secret item %s = (class=%q id=%q label=%q importedNil=%v)", wantID, it.Class, it.EntryID, it.Label, it.Imported == nil)
	}
	if !strings.Contains(it.RejectReason, wantKind) {
		t.Fatalf("secret item %s RejectReason %q missing kind %q", wantID, it.RejectReason, wantKind)
	}
}

func previewBlock(id, label, summary, body string) string {
	return "<!-- fugue:entry -->\n## " + label + "\n\n```yaml\nid: " + id +
		"\nkind: fact\nlabel: " + label +
		"\nsummary: " + summary +
		"\nkeywords: []\nanchors: []\nauthor: user\nverified: true\nvalidity: valid\ninvalid_reason: \"\"\nsuperseded_by: \"\"\nupdated_at: 2026-11-28T12:00:00Z\nrevision: 1\nbody_bytes: " + strconv.Itoa(len(body)) +
		"\n```\n" + body + "\n<!-- /fugue:entry -->\n\n"
}
