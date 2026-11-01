// lint:ok depguard

package depguard

import "log/slog"

func UseSlog() *slog.Logger { return slog.Default() }
