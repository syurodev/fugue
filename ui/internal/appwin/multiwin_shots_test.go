package appwin

import (
	"image"
	"image/draw"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/pkg/client"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	multiShotPrimaryID   = "sess-layout-review"
	multiShotSecondaryID = "sess-release-notes"
	multiShotDraftItem   = "multiwin-draft"
	multiShotDeltaText   = "The live draft is ready. Both windows are following this same update."
	testMsgKindUser      = "user_message"
	testMsgKindAssistant = "assistant_message"
)

type multiWindowShot struct {
	name string
	img  *image.RGBA
}

// TestMultiWindowShotsRender captures the two windows after Arrange moves
// the release-notes session and again after both windows open that session.
func TestMultiWindowShotsRender(t *testing.T) {
	t.Parallel()
	for _, shot := range renderMultiWindowShots(t) {
		if isUniformRGBA(shot.img) {
			t.Fatalf("multi-window shot %s rendered a blank image", shot.name)
		}
		shottest.Compare(t, shot.name, shot.img)
	}
}

func renderMultiWindowShots(t *testing.T) []multiWindowShot {
	t.Helper()
	manager := hostconn.NewManager(&hostconn.Config{})
	coordinator := newMultiWindow(manager, nil)
	primary, primaryUpdates := newMultiWindowShotScreens(manager)
	secondary, secondaryUpdates := newMultiWindowShotScreens(manager)
	coordinator.register(primaryUpdates)
	primary.openSession(multiShotPrimaryID)
	primary.applyResult(multiWindowShotPage(multiShotPrimaryID))
	primary.grid.openCompanion(SlotRef{Kind: KindSession, ID: multiShotSecondaryID})
	primary.grid.arrange(Pos{Col: 1, Row: 0}, arrangeNewWindow)
	primary.openSlotWindow(func(doc LayoutDoc) error {
		installNewLayout(secondary, "81ee8eac-c9e9-4452-877c-5225c342ffde", doc)
		coordinator.register(secondaryUpdates)
		return nil
	})
	if len(regularCells(primary.grid.doc)) != 0 || secondary.grid.mainRef().ID != multiShotSecondaryID ||
		secondary.sidebarVisible() {
		t.Fatal("Arrange did not move the selected slot from the primary grid to the secondary window with a collapsed sidebar")
	}
	secondary.ensureSessionTopics(multiShotSecondaryID)
	secondary.applyResult(multiWindowShotPage(multiShotSecondaryID))

	sourceSize, targetSize := image.Pt(960, shotWindowH), image.Pt(750, shotWindowH)
	source := renderMultiWindowShot(t, primary, sourceSize, "multiwin-primary")
	target := renderMultiWindowShot(t, secondary, targetSize, "multiwin-secondary")

	primary.openSession(multiShotSecondaryID)
	primary.applyResult(multiWindowShotPage(multiShotSecondaryID))
	coordinator.deliver(multiWindowShotDelta(t))
	for _, view := range []struct {
		sc      *screens
		updates *windowUpdates
	}{{primary, primaryUpdates}, {secondary, secondaryUpdates}} {
		view.sc.update(view.updates.queue.drain(), nil, nil, nil)
		assertMultiWindowShotDelta(t, view.sc)
	}

	left := renderMultiWindowShot(t, primary, sourceSize, "multiwin-synced-primary")
	right := renderMultiWindowShot(t, secondary, targetSize, "multiwin-synced-secondary")
	return []multiWindowShot{source, target, joinMultiWindowShots(left, right)}
}

func assertMultiWindowShotDelta(t *testing.T, sc *screens) {
	t.Helper()
	transcript := sc.transcripts[multiShotSecondaryID]
	if transcript == nil {
		t.Fatal("the window did not open the release-notes transcript")
	}
	items := transcript.Items()
	if len(items) != 2 || items[1].Display().Text != multiShotDeltaText {
		t.Fatal("the window did not apply the live draft to the release-notes transcript")
	}
}

func newMultiWindowShotScreens(manager *hostconn.Manager) (*screens, *windowUpdates) {
	sc := newScreens(manager, &fakeFetcher{}, session.NewPoster(manager.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "multiwin-shot"})
	createdAt := time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339)
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: multiShotPrimaryID, Vendor: client.VendorClaude, State: stateReady, CreatedAt: createdAt},
		{ID: multiShotSecondaryID, Vendor: client.VendorCodex, State: stateReady, CreatedAt: createdAt},
	}})
	return sc, &windowUpdates{queue: &updateQueue{}}
}

func multiWindowShotPage(id string) session.Result {
	prompt := "Check the layout after opening the release-notes session in a new window."
	reply := "The layout review stays here. Its sidebar is open and the main slot fills the grid."
	if id == multiShotSecondaryID {
		prompt = "Draft the release notes for the desktop update."
		reply = "The release notes cover saved layouts, the sidebar rail, and shared sessions."
	}
	return session.Result{
		Kind: session.ResultPage, Session: id,
		Items: []client.TranscriptItem{
			{ItemID: "multiwin-prompt", Kind: testMsgKindUser, Sequence: 1, Revision: 1, View: client.TranscriptView{Text: prompt}},
			{ItemID: multiShotDraftItem, Kind: testMsgKindAssistant, Sequence: 2, Revision: 1, View: client.TranscriptView{Text: reply}},
		},
	}
}

func multiWindowShotDelta(t *testing.T) hostconn.Update {
	t.Helper()
	payload, err := proto.Marshal(&transcriptv1.TranscriptDelta{
		ItemId: multiShotDraftItem, BaseRevision: 1, View: &transcriptv1.TranscriptView{Text: multiShotDeltaText},
	})
	if err != nil {
		t.Fatalf("encode the transcript update: %v", err)
	}
	return hostconn.Update{
		Topic: topicTranscriptDlt + multiShotSecondaryID,
		Event: &client.Event{Payload: payload},
	}
}

func renderMultiWindowShot(t *testing.T, sc *screens, size image.Point, name string) multiWindowShot {
	t.Helper()
	win, err := glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatalf("open headless window for %s: %v", name, err)
	}
	env := newInboxTestEnv(t)
	img := renderShotFrames(t, win, env, sc, size, name)
	if sc.lastErr != "" {
		t.Fatalf("multi-window shot %s recorded error: %s", name, sc.lastErr)
	}
	return multiWindowShot{name: name, img: img}
}

func joinMultiWindowShots(left, right multiWindowShot) multiWindowShot {
	const gutter = 12
	splitX := left.img.Bounds().Dx() + gutter
	width := splitX + right.img.Bounds().Dx()
	height := max(left.img.Bounds().Dy(), right.img.Bounds().Dy())
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.Black, image.Point{}, draw.Src)
	draw.Draw(img, left.img.Bounds(), left.img, image.Point{}, draw.Src)
	draw.Draw(img, right.img.Bounds().Add(image.Pt(splitX, 0)), right.img, image.Point{}, draw.Src)
	return multiWindowShot{name: "multiwin-synced-session", img: img}
}
