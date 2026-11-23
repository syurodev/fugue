package appwin

import (
	"fmt"
	"strings"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The screens' read side (F10.4a): one frame's topic deliveries, fetcher
// reads and reconnect re-arms folded into the screens' state. The screens
// type lives in screens.go; both files are one screens type. The composer's
// reads fold in screens_composer.go.

// update folds one frame's deliveries into the screens. The loops index
// rather than copy: the delivery and result structs are wide, and the
// window's hot path pays the copy every frame.
func (s *screens) update(updates []hostconn.Update, results []session.Result, posts []session.PostResult, muts []workspace.MutResult) {
	if !s.trackedOnce || s.grid.docGen != s.trackedGen {
		s.trackTopics()
	}
	for i := range updates {
		s.applyUpdate(updates[i])
	}
	for i := range results {
		s.applyResult(results[i])
	}
	for i := range posts {
		s.applyPostResult(posts[i])
	}
	for i := range muts {
		s.dlg.apply(&muts[i])
	}
	s.armWorkspaceReads()
}

// applyUpdate routes one topic delivery or connection-state change.
func (s *screens) applyUpdate(u hostconn.Update) {
	if s.applyConnectionUpdate(u.State) {
		return
	}
	switch u.Topic {
	case topicVendorSessions:
		s.applyVendorSessionsDelivery(u)
	case topicWorkspaces:
		s.applyWorkspaceDelivery(u)
	case topicVault:
		s.applyVaultDelivery(u)
	case topicInteraction:
		s.applyInteractionDelivery(u)
	case topicQuota:
		s.applyQuotaDelivery(u)
	case topicLogin:
		s.applyLoginDelivery(u)
	case topicAppState:
		s.applyAppStateDelivery(u)
		if u.Snapshot != nil {
			s.trackTopics()
		}
	case topicSecrets:
		s.applySecretsDelivery(u)
	default:
		if strings.HasPrefix(u.Topic, client.TopicTeamChannel) {
			s.applyTeamTopic(u)
			return
		}
		s.applyTranscriptUpdate(u)
	}
}

func (s *screens) applyConnectionUpdate(state hostconn.State) bool {
	switch state {
	case hostconn.StateConnected:
		s.reconnected()
		return true
	case hostconn.StateDisconnected, hostconn.StateIncompatible, hostconn.StateConnecting:
		s.clearTeamPayloads()
		return true
	default:
		return false
	}
}

func (s *screens) applyTranscriptUpdate(u hostconn.Update) {
	if id, delta := strings.CutPrefix(u.Topic, topicTranscriptDlt); delta {
		if t := s.transcripts[id]; s.slotSessions[id] && t != nil && u.Event != nil {
			s.fail(t.ApplyDelta(u.Event.Payload))
		}
		return
	}
	if id, durable := strings.CutPrefix(u.Topic, topicTranscript); durable && s.slotSessions[id] {
		s.applyTranscriptDelivery(id, u)
	}
}

func (s *screens) applyVendorSessionsDelivery(u hostconn.Update) {
	if u.Snapshot != nil {
		if err := s.sidebar.ApplySnapshot(u.Snapshot.State); err != nil {
			s.fail(err)
			return
		}
		s.sessionsLoaded = true
		s.prune()
		s.trackTopics()
	} else if u.Event != nil {
		s.applySessionEvent(u.Event.Type, u.Event.Payload)
	}
}

// reconnected arms the reads a fresh connection carries: the session list,
// the workspace list, the vendors' capabilities, and every visible session's
// newest page and commands. A request dropped while the host was down
// re-arms here.
func (s *screens) reconnected() {
	s.resetTeamPayloads()
	s.pendingItem = map[string]bool{}
	s.wsDetailReq = map[string]uint64{}
	s.fetcher.RequestSessions()
	s.wsReads.RequestWorkspaces()
	s.fetcher.RequestProbe()
	if s.vaultWorker != nil {
		req := vault.Request{Kind: vault.OpStatus}
		s.vaultWorker.Do(&req)
	}
	if s.inboxWorker != nil {
		req := inbox.Request{Kind: inbox.OpListPending}
		s.inboxWorker.Do(&req)
	}
	if s.accountsWorker != nil {
		req := accounts.Request{Kind: accounts.OpListAccounts}
		s.accountsWorker.Do(&req)
	}
	if s.settingsWorker != nil {
		req := settings.Request{Kind: settings.OpLoadPrefs}
		s.settingsWorker.Do(&req)
	}
	if s.secretsWorker != nil {
		req := secrets.Request{Kind: secrets.OpListSecrets}
		s.secretsWorker.Do(&req)
	}
	s.refreshVisibleTranscripts()
}

func (s *screens) refreshVisibleTranscripts() {
	for id := range s.slotSessions {
		delete(s.pendingPage, id)
		delete(s.pendingOlder, id)
		delete(s.pendingCommands, id)
		s.requestNewestPage(id)
		s.requestCommands(id)
	}
}

// applySessionEvent applies one vendor-sessions event. A created event
// carries no vendor or timestamps; the list read names them. The session the
// composer just started opens as soon as its created event names it.
func (s *screens) applySessionEvent(eventType string, payload []byte) {
	if err := s.sidebar.ApplyEvent(eventType, payload); err != nil {
		s.fail(err)
		return
	}
	switch eventType {
	case session.EventSessionCreated:
		s.fetcher.RequestSessions()
		if s.lastStarted != "" {
			if _, ok := s.sidebar.Lookup(s.lastStarted); ok {
				s.openSession(s.lastStarted)
				s.lastStarted = ""
			}
		}
	case session.EventSessionStateChanged:
		// The state change carries the commands revision; a list older than
		// what the topic announces is re-read for the open session (§9).
		if sess, ok := s.sidebar.Lookup(s.open); ok {
			if cmds := s.commands[s.open]; cmds == nil || sess.CommandsRevision > cmds.Revision {
				s.requestCommands(s.open)
			}
		}
	}
}

// applyTranscriptDelivery applies one durable transcript topic delivery.
func (s *screens) applyTranscriptDelivery(id string, u hostconn.Update) {
	t := s.transcripts[id]
	if t == nil {
		return
	}
	if u.Snapshot != nil {
		s.fail(t.ApplySnapshot(u.Snapshot.State))
		s.requestNewestPage(id)
		return
	}
	if u.Event != nil {
		s.fail(t.ApplyEvent(u.Event.Type, u.Event.Sequence, u.Event.Payload))
	}
}

// applyResult folds one fetcher read into the screens: the session list and
// transcript pages here, the composer's reads in applyComposerResult.
func (s *screens) applyResult(r session.Result) {
	switch r.Kind {
	case session.ResultSessions:
		if r.Err != nil {
			s.fail(fmt.Errorf("read the session list: %w", r.Err))
			return
		}
		s.sidebar.ApplyList(r.Sessions)
		s.sessionsLoaded = true
		s.prune()
		s.trackTopics()
		s.probeMissingVendors()
	case session.ResultPage:
		s.applyPageResult(&r)
	case session.ResultItem:
		delete(s.pendingItem, r.Session+":"+r.Detail.Item.ItemID)
		if r.Err != nil {
			s.fail(fmt.Errorf("read the transcript item: %w", r.Err))
			return
		}
		if t := s.transcripts[r.Session]; t != nil {
			t.ApplyItemDetail(&r.Detail)
		}
	case session.ResultCommands, session.ResultProbe, session.ResultWorkspaces, session.ResultWorkspace:
		s.applyComposerResult(r)
	}
}

func (s *screens) applyPageResult(r *session.Result) {
	t := s.transcripts[r.Session]
	if t == nil {
		return
	}
	if r.Older {
		delete(s.pendingOlder, r.Session)
	} else {
		delete(s.pendingPage, r.Session)
	}
	if r.Err != nil {
		s.fail(fmt.Errorf("read the transcript page: %w", r.Err))
		return
	}
	if r.Older {
		// Keep the reader's anchor: the prepended items shift the
		// content under the scroll offset by their height.
		if prepended := t.ApplyOlderPage(r.Items); prepended > 0 {
			s.viewFor(r.Session).AccountForPrepended(prepended)
		}
		return
	}
	t.ApplyNewestPage(r.Items)
	s.applyReopenAnchor(r.Session)
}
