package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	transcriptTestModel = "safe-model"
	absorbedNoticeCode  = "absorbed_into_vendor_turn"
)

func TestTranscriptPageRPCDefaultsTo200AndClampsAt500NewestFirst(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	recordPageItems(t, f, 600)
	defaultPage, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testSession})
	if err != nil || len(defaultPage) != 200 {
		t.Fatalf("default page = %d entries, %v; want 200", len(defaultPage), err)
	}
	if defaultPage[0].ID != "item-000600" || defaultPage[199].ID != "item-000401" {
		t.Fatalf("default page bounds = %q..%q; want newest first", defaultPage[0].ID, defaultPage[199].ID)
	}
	largePage, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testSession, Limit: 900})
	if err != nil || len(largePage) != 500 {
		t.Fatalf("clamped page = %d entries, %v; want 500", len(largePage), err)
	}
	if largePage[0].ID != "item-000600" || largePage[499].ID != "item-000101" {
		t.Fatalf("clamped page bounds = %q..%q; want newest first", largePage[0].ID, largePage[499].ID)
	}
}

func TestTranscriptItemRPCReturnsFullRedactedContent(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, func(context.Context) ([]secrets.KnownValue, error) {
		return []secrets.KnownValue{{Value: redact.NewSecret(knownAPIToken), Kind: secrets.KindCredential}}, nil
	})
	f.awaitKnownValues()
	text := strings.Repeat("a", 65<<10) + knownAPIToken + " tail"
	item := transcript.Item{
		ID: testItem, SessionID: testSession, TurnID: testTurn, Kind: transcript.AssistantMessage,
		Metadata: transcript.AssistantMessageMetadata{ModelID: transcriptTestModel},
		Content:  transcript.AssistantMessageContent{Text: text},
	}
	if err := f.service.RecordItem(t.Context(), testSession, 2, &item, nil); err != nil {
		t.Fatal(err)
	}
	detail, err := f.service.TranscriptItemFor(t.Context(), string(KindApp), testSession, testItem)
	if err != nil {
		t.Fatal(err)
	}
	var content transcript.AssistantMessageContent
	if err := json.Unmarshal(detail.Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content.Text) <= 64<<10 || strings.Contains(content.Text, knownAPIToken) || !strings.Contains(content.Text, "tail") {
		t.Fatalf("detail text length=%d token-present=%t tail-present=%t", len(content.Text), strings.Contains(content.Text, knownAPIToken), strings.Contains(content.Text, "tail"))
	}
}

func TestTranscriptRPCsWhileVaultLockedReturnMetadataOnly(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	item := pendingToolCall(testItem, "protected output")
	if err := f.service.RecordItem(t.Context(), testSession, 2, &item, nil); err != nil {
		t.Fatal(err)
	}
	f.vault.Lock()
	page, err := f.service.TranscriptPageFor(t.Context(), string(KindApp), TranscriptPageParams{Session: testSession})
	if err != nil || len(page) != 1 || !page[0].ContentLocked || page[0].View.Text != "" {
		t.Fatalf("locked page = %+v, %v; want metadata only", page, err)
	}
	detail, err := f.service.TranscriptItemFor(t.Context(), string(KindApp), testSession, testItem)
	if err != nil || !detail.ContentLocked || len(detail.Content) != 0 || detail.View.Text != "" {
		t.Fatalf("locked detail = %+v, %v; want metadata only", detail, err)
	}
	if _, err := f.service.TranscriptPageFor(t.Context(), adapterSession, TranscriptPageParams{Session: testSession}); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("session principal page error = %v, want permission denied", err)
	}
	if _, err := f.service.TranscriptItemFor(t.Context(), "unknown", testSession, testItem); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("unknown principal item error = %v, want permission denied", err)
	}
}

func TestTranscriptRPCsDenyNonLocalPrincipal(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	for _, principal := range []string{adapterSession, "unknown"} {
		if _, err := f.service.TranscriptPageFor(t.Context(), principal, TranscriptPageParams{Session: testSession}); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("principal %q page error = %v, want permission denied", principal, err)
		}
		if _, err := f.service.TranscriptItemFor(t.Context(), principal, testSession, testItem); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("principal %q item error = %v, want permission denied", principal, err)
		}
	}
}

func TestUnknownTranscriptSessionAndItemAreTyped(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	if _, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: "unknown-session"}); errcode.CodeOf(err) != errcode.TranscriptSessionNotFound {
		t.Fatalf("unknown session page error = %v", err)
	}
	if _, err := f.service.TranscriptItem(t.Context(), testSession, "unknown-item"); errcode.CodeOf(err) != errcode.TranscriptItemNotFound {
		t.Fatalf("unknown item error = %v", err)
	}
}

func TestVendorOriginTurnsAreGroupedAndAbsorbedNoticeStaysOnFugueTurn(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	vendorTurn := "vendor-3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	if _, err := f.store.Append(t.Context(), testSession, 2, &vendorstore.Event{
		Type: vendorstore.VendorTurnStarted, TurnID: vendorTurn, TurnOrigin: vendorstore.TurnOriginVendor,
	}); err != nil {
		t.Fatal(err)
	}
	vendorItem := transcript.Item{
		ID: "vendor-notice", SessionID: testSession, TurnID: vendorTurn, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: testNoticeLevel, Code: "vendor"}, Content: transcript.NoticeContent{Text: "vendor generated"},
	}
	absorbedItem := transcript.Item{
		ID: "absorbed-notice", SessionID: testSession, TurnID: testTurn, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: testNoticeLevel, Code: absorbedNoticeCode}, Content: transcript.NoticeContent{Text: "absorbed"},
	}
	if err := f.service.RecordItem(t.Context(), testSession, 3, &vendorItem, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RecordItem(t.Context(), testSession, 4, &absorbedItem, nil); err != nil {
		t.Fatal(err)
	}
	assertVendorTurnOrigins(t, f, vendorTurn)
}

func assertVendorTurnOrigins(t *testing.T, f *transcriptFixture, vendorTurn string) {
	t.Helper()
	page, err := f.service.TranscriptPage(t.Context(), TranscriptPageParams{Session: testSession})
	if err != nil || len(page) != 2 {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if page[0].ID != "absorbed-notice" || page[0].TurnID != testTurn || page[0].TurnOrigin != vendorstore.TurnOriginFugue || page[0].Metadata.Code != absorbedNoticeCode {
		t.Fatalf("absorbed notice = %+v; want on Fugue turn", page[0])
	}
	if page[1].ID != "vendor-notice" || page[1].TurnID != vendorTurn || page[1].TurnOrigin != vendorstore.TurnOriginVendor {
		t.Fatalf("vendor notice = %+v; want vendor-origin turn", page[1])
	}
}
