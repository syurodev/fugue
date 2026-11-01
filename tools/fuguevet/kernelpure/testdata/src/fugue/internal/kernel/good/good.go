package good

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrRejected = errors.New("rejected")

var _ fmt.Stringer = state(0)

type state int

func (s state) String() string { return fmt.Sprintf("state-%d", int(s)) }

const timeout = 5 * time.Second

func Deadline(at time.Time) time.Time { return at.Add(timeout) }

func Wrap(err error) error { return fmt.Errorf("kernel: %w", err) }

func Upper(s string) string { return strings.ToUpper(s) }

func Since(a, b time.Time) time.Duration { return b.Sub(a) }
