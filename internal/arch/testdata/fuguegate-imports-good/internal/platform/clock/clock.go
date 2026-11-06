package clock

import (
	"context"
	"fmt"
	"time"
)

type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Sleep waits for d on the timer, or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sleep interrupted: %w", context.Cause(ctx))
	}
}
