package appwin

import (
	"testing"
)

type topicRecorder struct{ active map[string]bool }

func (r *topicRecorder) set(topic string, active bool) { r.active[topic] = active }

func (r *topicRecorder) subscribed(topic string) bool { return r.active[topic] }

func newTrackGateScreens(t *testing.T) (*screens, *topicRecorder) {
	t.Helper()
	sc, _ := newTestScreens(t)
	rec := &topicRecorder{active: map[string]bool{}}
	sc.setTopic = rec.set
	sc.applyWindowRecord(nil)
	return sc, rec
}

func TestTrackTopicsRunsOnlyWhenGridDocumentChanges(t *testing.T) {
	t.Parallel()
	sc, rec := newTrackGateScreens(t)
	sc.update(nil, nil, nil, nil)

	sc.grid.openMain(SlotRef{Kind: KindSession, ID: testMultiSessionOne})
	sc.update(nil, nil, nil, nil)
	topic := topicTranscript + testMultiSessionOne
	if !rec.subscribed(topic) {
		t.Fatal("an edit did not subscribe the new session on the next update")
	}

	// Without a grid change a frame must not re-run trackTopics: drop the
	// subscription state and confirm update does not restore it.
	delete(sc.slotSessions, testMultiSessionOne)
	rec.active = map[string]bool{}
	sc.update(nil, nil, nil, nil)
	if rec.subscribed(topic) || sc.slotSessions[testMultiSessionOne] {
		t.Fatal("update re-ran trackTopics without a grid change")
	}

	sc.grid.openMain(SlotRef{Kind: KindSession, ID: testMultiSessionTwo})
	sc.update(nil, nil, nil, nil)
	if !rec.subscribed(topicTranscript + testMultiSessionTwo) {
		t.Fatal("a second edit did not re-track")
	}
}

func TestTrackTopicsRunsAfterStoredLayoutApplied(t *testing.T) {
	t.Parallel()
	sc, rec := newTrackGateScreens(t)
	sc.update(nil, nil, nil, nil)
	before := sc.grid.docGen

	doc := cloneLayout(sc.grid.doc)
	doc.Columns[0].Slots[0].Ref = SlotRef{Kind: KindSession, ID: testMultiSessionTwo}
	sc.grid.applyStored(DecodeLayoutResult{Doc: doc}, 3)
	if sc.grid.docGen == before {
		t.Fatal("applyStored did not bump docGen")
	}
	sc.update(nil, nil, nil, nil)
	if !rec.subscribed(topicTranscript + testMultiSessionTwo) {
		t.Fatal("the stored layout's session was not tracked on the next update")
	}
}
