package app

import (
	"encoding/json"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// Pure helpers of the team budgets loop (team_budgets.go), kept apart so the
// loop file stays under the 500-line limit.

func buildSendMailbox(recipientTaskID string, item mailboxDigestItem) ([]vendorstore.MailboxEnqueueInput, error) {
	if recipientTaskID == "" {
		return nil, nil
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal mailbox item", err)
	}
	return []vendorstore.MailboxEnqueueInput{{RecipientTaskID: recipientTaskID, Kind: vendorstore.MailboxKindSend, Payload: raw}}, nil
}

func allWatchesSilent(watched []*silenceWatch, now time.Time, timeout time.Duration) bool {
	for _, w := range watched {
		if now.Sub(w.lastActivity) < timeout {
			return false
		}
	}
	return len(watched) > 0
}
