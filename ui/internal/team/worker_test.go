package team

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type channelHost struct {
	Client
	post client.PostChannelMessageInput
}

func (h *channelHost) PostChannelMessage(_ context.Context, input *client.PostChannelMessageInput) (int64, error) {
	h.post = *input
	return 1, nil
}

func TestWorkerPassesThreadAndExplicitTargetToHost(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"", "agent-2"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			host := new(channelHost)
			wake := make(chan struct{}, 1)
			w := Start(t.Context(), func() Client { return host }, func() { wake <- struct{}{} }, func() string { return "id-1" })
			t.Cleanup(w.Stop)
			if !w.Submit(&Request{Op: Post, Team: "team-1", Thread: "agent-1", Task: target, Text: "hello"}) {
				t.Fatal("worker rejected the request")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			select {
			case <-wake:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			got := w.Drain()
			if len(got) != 1 || got[0].Err != nil {
				t.Fatalf("delivered results = %v", got)
			}
			want := client.PostChannelMessageInput{CommandID: "ui-team-id-1", TeamID: "team-1", ThreadTaskID: "agent-1", ToTaskID: target, Text: "hello"}
			if diff := cmp.Diff(want, host.post); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestMergeChannelReplacesReplayAndKeepsOlderPage(t *testing.T) {
	t.Parallel()
	old := []client.ChannelLine{{ChannelSeq: 1}, {ChannelSeq: 2}}
	delivered := []client.ChannelLine{{ChannelSeq: 3}, {ChannelSeq: 2, Content: "available after unlock"}}
	want := []client.ChannelLine{old[0], delivered[1], delivered[0]}
	if diff := cmp.Diff(want, MergeChannel(old, delivered)); diff != "" {
		t.Fatal(diff)
	}
}
