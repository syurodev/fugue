package hostapi

import (
	"context"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

func localSessionPrincipal(ctx context.Context, backend app.Backend, purpose string) (string, error) {
	st, err := connectionState(ctx)
	if err != nil {
		return "", toConnectError(ctx, backend, err)
	}
	if err := requireHelloed(ctx); err != nil {
		return "", toConnectError(ctx, backend, err)
	}
	principal := string(st.principal())
	if !app.IsLocalPrincipal(principal) {
		return "", toConnectError(ctx, backend, errcode.New(errcode.PermissionDenied, "only local principals may use "+purpose))
	}
	return principal, nil
}
