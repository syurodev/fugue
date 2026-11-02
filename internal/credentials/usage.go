package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// SaveUsage merges what a response's rate-limit headers said into the stored
// readings, by label. A reading that is stale for every window it carries, and
// an empty list, change nothing: usage and its observed time stay as they were.
func (b *Book) SaveUsage(ctx context.Context, id string, windows []LimitWindow) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "save usage failed", err)
	}
	defer rollbackQuietly(tx)
	var stored string
	err = tx.QueryRowContext(ctx, "SELECT usage FROM credential_accounts WHERE id = ?", id).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return errcode.New(errcode.NotFound, "account not found")
	}
	if err != nil {
		return errcode.Wrap(errcode.Internal, "save usage failed", err)
	}
	var old []LimitWindow
	if err = json.Unmarshal([]byte(stored), &old); err != nil {
		return errcode.Wrap(errcode.Internal, "decode the account usage failed", err)
	}
	merged, accepted := mergeWindows(old, windows)
	if !accepted {
		return nil
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode the account usage failed", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE credential_accounts SET usage = ?, observed_at = ? WHERE id = ?",
		string(encoded), millis(b.clock.Now()), id); err != nil {
		return errcode.Wrap(errcode.Internal, "save usage failed", err)
	}
	if err = tx.Commit(); err != nil {
		return errcode.Wrap(errcode.Internal, "save usage failed", err)
	}
	return nil
}

func rollbackQuietly(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil {
		return
	}
}

// mergeWindows returns old with each of fresh replacing the window of the same
// label, in old's order, new labels last, and whether any fresh window was
// accepted.
//
// A reading is not replaced by an older one of the same window: usage only
// grows within one window, and responses can arrive out of order, so a lower
// percentage with the same label and the same known reset is the stale one and
// the stored window wins. A different reset is a new window, and a window with
// no known reset (ResetsAt 0) cannot be told apart, so both replace.
func mergeWindows(old, fresh []LimitWindow) ([]LimitWindow, bool) {
	out := append([]LimitWindow(nil), old...)
	accepted := false
	for _, w := range fresh {
		replaced := false
		for i := range out {
			if out[i].Label != w.Label {
				continue
			}
			stale := w.ResetsAt != 0 && out[i].ResetsAt == w.ResetsAt && w.UsedPercent < out[i].UsedPercent
			if !stale {
				out[i] = w
				accepted = true
			}
			replaced = true
			break
		}
		if !replaced {
			out = append(out, w)
			accepted = true
		}
	}
	return out, accepted
}
