package clock

import (
	"time"

	_ "github.com/syurodev/fugue/internal/platform/redact"
)

type Real struct{}

func (Real) Now() time.Time { return time.Now() }
