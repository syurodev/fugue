package hostapi

import (
	"context"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

type heavySlotAcquirer interface {
	AcquireHeavySlot(ctx context.Context) (release func(), done <-chan struct{}, err error)
}

func acquireAndHoldHeavySlot(ctx context.Context, backend app.Backend, sendAcquired func() error) error {
	acquirer, ok := backend.(heavySlotAcquirer)
	if !ok {
		return toConnectError(ctx, backend, errcode.New(errcode.Unavailable, "heavy slot service is unavailable"))
	}
	release, done, err := acquirer.AcquireHeavySlot(ctx)
	if err != nil {
		return toConnectError(ctx, backend, err)
	}
	defer release()
	if err := sendAcquired(); err != nil {
		return errcode.Wrap(errcode.Internal, "send heavy slot grant", err)
	}
	select {
	case <-ctx.Done():
	case <-done:
	}
	return nil
}
