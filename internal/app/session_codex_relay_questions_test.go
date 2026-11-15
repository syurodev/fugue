package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
)

func TestCodexUserInputRequestBecomesARelayedQuestion(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	const envQuestionID = "env"
	const optCanary = "canary"
	const optLive = "live-cluster"
	qFrame := fmt.Sprintf(`{"id":401,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"ask-1","isBlocking":true,"questions":[{"id":%q,"header":"Target","question":"Select target environment","isOther":false,"isSecret":false,"options":[{"label":%q,"description":"Canary cluster"},{"label":%q,"description":"Live cluster"}]}]}}`,
		codex.MethodRequestUserInput, fakeCodexThreadID, envQuestionID, optCanary, optLive)
	f.sendServerRequestTurn(t, "codex-question-turn", qFrame)

	prompt := awaitRequested(t, sub)
	if prompt.Kind != string(interaction.KindQuestion) || len(prompt.Questions) != 1 {
		t.Fatalf("relayed question prompt = %+v, want KindQuestion with 1 question", prompt)
	}
	wantQuestion := InteractionQuestion{
		QuestionID: envQuestionID,
		Prompt:     "Select target environment",
		AllowText:  true,
		Options: []InteractionOption{
			{ID: optCanary, Label: optCanary, Description: "Canary cluster"},
			{ID: optLive, Label: optLive, Description: "Live cluster"},
		},
	}
	if diff := cmp.Diff(wantQuestion, prompt.Questions[0]); diff != "" {
		t.Fatalf("relayed question (-want +got):\n%s", diff)
	}
	if _, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-q-401", Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: prompt.RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: envQuestionID, OptionIDs: []string{optCanary}}},
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = sub.Next(t.Context())
	resp := f.awaitRPCResponse(t, "401")
	if resp.Error != nil || string(resp.Result) != `{"answers":{"env":{"answers":["canary"]}}}` {
		t.Fatalf("RPC 401 response = %+v, want canary answer", resp)
	}
	if diff := cmp.Diff([]string{"answered"}, f.recordedDecisions(t, "Select target environment", optCanary)); diff != "" {
		t.Fatalf("recorded question decisions (-want +got):\n%s", diff)
	}
	assertPartialUserInputOmitsUnansweredQuestion(t, envQuestionID, optCanary)
}

func assertPartialUserInputOmitsUnansweredQuestion(t *testing.T, envQuestionID, optCanary string) {
	t.Helper()
	partialLine := fmt.Sprintf(`{"id":402,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"ask-2","isBlocking":true,"questions":[{"id":%q,"header":"Target","question":"Select target environment","isOther":false,"isSecret":false,"options":[{"label":%q,"description":"Canary cluster"}]},{"id":"notes","header":"Notes","question":"Optional release notes","isOther":true,"isSecret":false,"options":[]}]}}`,
		codex.MethodRequestUserInput, fakeCodexThreadID, envQuestionID, optCanary)
	partialSess, partialDir, _ := startHelperCodexWithRequests(t, "partial-q", partialLine, 1)
	partialChoice, ok := translateCodexRelayedOutcome(&interaction.Ticket{
		Outcome: interaction.OutcomeAnswered,
		Answers: []interaction.Answer{
			{QuestionID: envQuestionID, OptionIDs: []string{optCanary}},
			{QuestionID: "notes"},
		},
	}, vendor.ServerRequestQuestion)
	if !ok {
		t.Fatal("translateCodexRelayedOutcome returned false for partial question")
	}
	if err := partialSess.Resolve(t.Context(), vendor.ApprovalRef{ID: "402"}, partialChoice.decision); err != nil {
		t.Fatalf("Resolve partial question: %v", err)
	}
	partialRaw := readRawRPCResponseLine(t, partialDir, "402")
	var partialResp codexRecordedResponse
	if err := json.Unmarshal(partialRaw, &partialResp); err != nil {
		t.Fatalf("unmarshal partial response: %v", err)
	}
	if partialResp.Error != nil || string(partialResp.Result) != `{"answers":{"env":{"answers":["canary"]}}}` {
		t.Fatalf("partial RPC 402 response = %s, want unanswered notes omitted", partialRaw)
	}
}

func TestCodexRetriedQuestionReplaysTheSameAnswers(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	const qAnsweredID, qPartlyID, qDeniedID = "501", "502", "503"
	makeFrame := func(id, itemID string) string {
		return fmt.Sprintf(`{"id":%s,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":%q,"isBlocking":true,"questions":[{"id":"q1","header":"Q1","question":"First choice","isOther":true,"isSecret":false,"options":[{"label":"opt-a","description":"A"}]},{"id":"q2","header":"Q2","question":"Second choice","isOther":true,"isSecret":false,"options":[{"label":"opt-b","description":"B"}]}]}}`,
			id, codex.MethodRequestUserInput, fakeCodexThreadID, itemID)
	}
	lines := map[string]string{
		qAnsweredID: makeFrame(qAnsweredID, "ask-501"),
		qPartlyID:   makeFrame(qPartlyID, "ask-502"),
		qDeniedID:   makeFrame(qDeniedID, "ask-503"),
	}

	resolveRelayedQuestionCase(t, f, sub, "q-retry-1", qAnsweredID, lines[qAnsweredID], OutcomeAnswered,
		[]InteractionAnswer{{QuestionID: "q1", OptionIDs: []string{"opt-a"}}, {QuestionID: "q2", OptionIDs: []string{"opt-b"}}},
		`{"answers":{"q1":{"answers":["opt-a"]},"q2":{"answers":["opt-b"]}}}`,
	)
	resolveRelayedQuestionCase(t, f, sub, "q-retry-3", qDeniedID, lines[qDeniedID], OutcomeDenied, nil, `{"answers":{}}`)

	turnID, err := f.tools.recorder.ActiveTurnID(t.Context(), f.binding.SessionID)
	if err != nil || turnID == "" {
		t.Fatalf("ActiveTurnID = (%q, %v)", turnID, err)
	}
	firstLines := map[string][]byte{
		qAnsweredID: readRawRPCResponseLine(t, f.rootDir, qAnsweredID),
		qPartlyID:   seedAndResolvePartlyAnsweredQuestion(t, f, turnID, qPartlyID, lines[qPartlyID]),
		qDeniedID:   readRawRPCResponseLine(t, f.rootDir, qDeniedID),
	}

	allLines := strings.Join([]string{lines[qAnsweredID], lines[qPartlyID], lines[qDeniedID]}, "\n")
	replaySess, replayDir, replayReqs := startHelperCodexWithRequests(t, "replay", allLines, 3)
	fallbackSess, fallbackDir, fallbackReqs := startHelperCodexWithRequests(t, "fallback", allLines, 3)
	for _, id := range []string{qAnsweredID, qPartlyID, qDeniedID} {
		assertQuestionReplayMatches(t, f, turnID, id, firstLines[id], replaySess, replayDir, replayReqs[id], fallbackSess, fallbackDir, fallbackReqs[id])
	}
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("pending after replay = (%+v, %v), want 0", pending, pErr)
	}
	if ev, hasEv := sub.popEvent(); hasEv {
		t.Fatalf("unexpected interaction event during replay: %+v", ev)
	}
}

func resolveRelayedQuestionCase(
	t *testing.T, f *codexRelayFixture, sub *Subscription,
	cmdPrefix, id, frame, outcome string, answers []InteractionAnswer, wantResult string,
) {
	t.Helper()
	if cmdPrefix == "q-retry-1" {
		f.sendServerRequestTurn(t, cmdPrefix, frame)
	} else {
		f.steerServerRequest(t, cmdPrefix, frame)
	}
	prompt := awaitRequested(t, sub)
	if _, err := f.inter.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID: "cmd-" + cmdPrefix, Principal: testAppPrincipal, Incarnation: f.inter.Incarnation(),
		RequestID: prompt.RequestID, Outcome: outcome, Answers: answers,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = sub.Next(t.Context())
	resp := f.awaitRPCResponse(t, id)
	if resp.Error != nil || string(resp.Result) != wantResult {
		t.Fatalf("id %s response = %+v, want %s", id, resp, wantResult)
	}
}

func seedAndResolvePartlyAnsweredQuestion(t *testing.T, f *codexRelayFixture, turnID, id, line string) []byte {
	t.Helper()
	sess, dir, reqs := startHelperCodexWithRequests(t, "partly-first", line, 1)
	req := reqs[id]
	choice, ok := translateCodexRelayedOutcome(&interaction.Ticket{
		Outcome: interaction.OutcomeAnswered,
		Answers: []interaction.Answer{{QuestionID: "q1", OptionIDs: []string{"opt-a"}}},
	}, vendor.ServerRequestQuestion)
	if !ok {
		t.Fatal("translateCodexRelayedOutcome returned false for partly answered question")
	}
	opID, err := interaction.VendorOperationKey(f.binding.SessionID, turnID, req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	f.tools.tombstones.putCodex(
		f.binding.SessionID, opID, approveArgsDigest(req.Method, req.Detail),
		choice.decision.Allow, choice.selectedOption, choice.decision.Answers,
	)
	if rErr := sess.Resolve(t.Context(), vendor.ApprovalRef{ID: req.RequestID}, choice.decision); rErr != nil {
		t.Fatalf("first partly Resolve: %v", rErr)
	}
	raw := readRawRPCResponseLine(t, dir, id)
	if !bytes.Contains(raw, []byte(`"answers":{"q1":{"answers":["opt-a"]}}`)) || bytes.Contains(raw, []byte(`"q2"`)) {
		t.Fatalf("unexpected first partly JSON-RPC bytes: %s", raw)
	}
	return raw
}

func assertQuestionReplayMatches(
	t *testing.T, f *codexRelayFixture, turnID, id string, firstRaw []byte,
	replaySess *codex.Session, replayDir string, req *vendor.ServerRequest,
	fallbackSess *codex.Session, fallbackDir string, fbReq *vendor.ServerRequest,
) {
	t.Helper()
	f.tools.RelayServerRequest(t.Context(), f.session.ID, req, replaySess)
	if replayRaw := readRawRPCResponseLine(t, replayDir, id); !bytes.Equal(replayRaw, firstRaw) {
		t.Fatalf("id %s replay JSON-RPC mismatch:\nfirst:  %s\nreplay: %s", id, firstRaw, replayRaw)
	}
	opID, kErr := interaction.VendorOperationKey(f.binding.SessionID, turnID, fbReq.RequestID)
	if kErr != nil {
		t.Fatal(kErr)
	}
	choice, fbOK := f.tools.fallbackCodexRelayedTombstone(
		errcode.New(errcode.InteractionNotFound, "ticket already taken"),
		f.binding, opID, approveArgsDigest(fbReq.Method, fbReq.Detail), fbReq.Kind,
	)
	if !fbOK {
		t.Fatalf("id %s fallbackCodexRelayedTombstone returned false", id)
	}
	if rErr := fallbackSess.Resolve(t.Context(), vendor.ApprovalRef{ID: fbReq.RequestID}, choice.decision); rErr != nil {
		t.Fatalf("id %s fallback Resolve: %v", id, rErr)
	}
	if fallbackRaw := readRawRPCResponseLine(t, fallbackDir, id); !bytes.Equal(fallbackRaw, firstRaw) {
		t.Fatalf("id %s fallback JSON-RPC mismatch:\nfirst:    %s\nfallback: %s", id, firstRaw, fallbackRaw)
	}
}

func startHelperCodexWithRequests(t *testing.T, tag, joinedLines string, want int) (*codex.Session, string, map[string]*vendor.ServerRequest) {
	t.Helper()
	exe := mustExe(t)
	binDir := t.TempDir()
	codexLink := filepath.Join(binDir, "codex")
	if err := os.Symlink(exe, codexLink); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	sess, err := codex.NewAdapter(nil, codex.WithBinary(codexLink)).Start(t.Context(), &vendor.StartSpec{
		Cwd:       cwd,
		SessionID: "helper-" + tag,
	})
	if err != nil {
		t.Fatalf("Start helper %s: %v", tag, err)
	}
	t.Cleanup(func() {
		if _, cErr := sess.Close(context.Background(), vendor.CloseUserRequest); cErr != nil {
			t.Errorf("close helper codex %s: %v", tag, cErr)
		}
	})
	codexSess, ok := sess.(*codex.Session)
	if !ok {
		t.Fatalf("expected *codex.Session, got %T", sess)
	}
	if _, sErr := codexSess.Send(t.Context(), vendor.Input{Text: "server-request:" + joinedLines}); sErr != nil {
		t.Fatalf("helper Send(%s): %v", tag, sErr)
	}
	reqs := make(map[string]*vendor.ServerRequest, want)
	for len(reqs) < want {
		obs, ok := <-codexSess.Observations()
		if !ok {
			t.Fatalf("helper %s observations closed early after %d requests", tag, len(reqs))
		}
		if obs.ServerRequest != nil {
			reqCopy := *obs.ServerRequest
			reqs[reqCopy.RequestID] = &reqCopy
		}
	}
	return codexSess, cwd, reqs
}

func readRawRPCResponseLine(t *testing.T, dir, id string) []byte {
	t.Helper()
	var found []byte
	awaitActor(t, func() bool {
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, fakeCodexStdinFile)))
		if err != nil {
			return false
		}
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			var msg codexRecordedResponse
			if json.Unmarshal(trimmed, &msg) == nil && msg.Method == "" && string(msg.ID) == id {
				found = trimmed
				return true
			}
		}
		return false
	})
	return found
}

func TestCodexSecretQuestionFailsClosedWithoutATicket(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	const (
		canaryHeader   = "PRIVATE-CANARY-HEADER-909"
		canaryQuestion = "PRIVATE-CANARY-QUESTION-909"
		canaryOption   = "PRIVATE-CANARY-OPTION-909"
		publicQuestion = "PUBLIC-COUSIN-QUESTION-909"
		wantNoticeText = "Codex asked for secret input, which Fugue cannot relay yet"
	)
	secretFrame := fmt.Sprintf(`{"id":601,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"ask-secret","isBlocking":true,"questions":[{"id":"q-pub","header":"Pub","question":%q,"isOther":false,"isSecret":false,"options":[]},{"id":"q-sec","header":%q,"question":%q,"isOther":true,"isSecret":true,"options":[{"label":%q,"description":"masked"}]}]}}`,
		codex.MethodRequestUserInput, fakeCodexThreadID, publicQuestion, canaryHeader, canaryQuestion, canaryOption)
	f.sendServerRequestTurn(t, "codex-secret-turn", secretFrame)

	resp := f.awaitRPCResponse(t, "601")
	if resp.Error == nil || len(resp.Result) != 0 || !strings.Contains(resp.Error.Message, wantNoticeText) {
		t.Fatalf("RPC 601 response = %+v, want JSON-RPC error %q with no result", resp, wantNoticeText)
	}
	if pending, _, pErr := f.inter.ListPending(t.Context(), testAppPrincipal); pErr != nil || len(pending) != 0 {
		t.Fatalf("ListPending = (%+v, %v), want 0 tickets for secret question", pending, pErr)
	}
	if ev, hasEv := sub.popEvent(); hasEv {
		t.Fatalf("unexpected interaction event for secret question: %+v", ev)
	}
	canaries := []string{canaryHeader, canaryQuestion, canaryOption, publicQuestion}
	assertSecretUnsupportedNoticeInTranscript(t, f, wantNoticeText, canaries)
	if got := len(f.recordedDecisions(t, canaries...)); got != 0 {
		t.Fatalf("ApprovalDecided count = %d, want 0", got)
	}
	assertDataDirContainsNoCanaries(t, f.hp.deps.DataDir, canaries)
}

func assertSecretUnsupportedNoticeInTranscript(t *testing.T, f *codexRelayFixture, wantNoticeText string, canaries []string) {
	t.Helper()
	var notice *TranscriptEntry
	awaitActor(t, func() bool {
		page, err := f.rt.transcripts.TranscriptPage(t.Context(), TranscriptPageParams{Session: f.session.ID, Limit: 50})
		if err != nil {
			return false
		}
		for i := range page {
			if page[i].Kind == transcript.Notice && page[i].View.Text == wantNoticeText {
				entry := page[i]
				notice = &entry
				return true
			}
		}
		return false
	})
	if notice.Metadata.Code != "unsupported_event" {
		t.Fatalf("notice metadata code = %q, want unsupported_event", notice.Metadata.Code)
	}
	detail, dErr := f.rt.transcripts.TranscriptItem(t.Context(), f.session.ID, notice.ID)
	if dErr != nil {
		t.Fatalf("TranscriptItem(%s): %v", notice.ID, dErr)
	}
	for _, needle := range canaries {
		if strings.Contains(string(detail.Content), needle) {
			t.Fatalf("secret question canary %q in transcript item content: %s", needle, detail.Content)
		}
	}
}

func assertDataDirContainsNoCanaries(t *testing.T, dataDir string, canaries []string) {
	t.Helper()
	root, oErr := os.OpenRoot(dataDir)
	if oErr != nil {
		t.Fatalf("OpenRoot(%s): %v", dataDir, oErr)
	}
	defer func() {
		if cErr := root.Close(); cErr != nil {
			t.Errorf("close root: %v", cErr)
		}
	}()
	if err := fs.WalkDir(root.FS(), ".", func(relPath string, d fs.DirEntry, wErr error) error {
		if wErr != nil || d.IsDir() {
			return wErr
		}
		data, rErr := root.ReadFile(relPath)
		if rErr != nil {
			return fmt.Errorf("read %s: %w", relPath, rErr)
		}
		for _, needle := range canaries {
			if bytes.Contains(data, []byte(needle)) {
				t.Fatalf("secret question canary %q persisted in %s", needle, relPath)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCommandApprovalRelaysExecpolicyAmendment(t *testing.T) {
	t.Parallel()
	f := newCodexRelayFixture(t)
	sub := f.subscribeInteractions(t)

	cmdFrame := fmt.Sprintf(`{"id":602,"method":%q,"params":{"threadId":%q,"turnId":"t-1","itemId":"call-amend","command":"git status","cwd":"/workspace","proposedExecpolicyAmendment":["git","status"]}}`,
		codex.MethodCommandApproval, fakeCodexThreadID)
	f.sendServerRequestTurn(t, "codex-execpolicy-turn", cmdFrame)

	prompt := awaitRequested(t, sub)
	wantOpts := []InteractionOption{
		{ID: codex.DecisionAccept, Label: codex.DecisionAccept},
		{ID: codex.DecisionAcceptForSession, Label: codex.DecisionAcceptForSession},
		{ID: codex.DecisionAcceptWithExecpolicyAmendment, Label: codex.DecisionAcceptWithExecpolicyAmendment},
		{ID: codex.DecisionDecline, Label: codex.DecisionDecline},
		{ID: codex.DecisionCancel, Label: codex.DecisionCancel},
	}
	if diff := cmp.Diff(wantOpts, prompt.Options); diff != "" {
		t.Fatalf("command approval options (-want +got):\n%s", diff)
	}
	if !strings.Contains(prompt.Detail, `"proposedExecpolicyAmendment":["git","status"]`) {
		t.Fatalf("detail missing proposedExecpolicyAmendment: %s", prompt.Detail)
	}
	f.answerOption(t, "cmd-amend-602", prompt.RequestID, codex.DecisionAcceptWithExecpolicyAmendment)
	_, _ = sub.Next(t.Context())
	resp := f.awaitRPCResponse(t, "602")
	if resp.Error != nil || string(resp.Result) != `{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}}` {
		t.Fatalf("RPC 602 response = %+v, want execpolicy amendment decision object", resp)
	}
}
