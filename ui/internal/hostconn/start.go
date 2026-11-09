package hostconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/syurodev/fugue/pkg/client"
)

// ErrStartupTimeout means the host did not become ready within ten seconds.
var ErrStartupTimeout = errors.New("host startup timed out")

type dialResult struct {
	conn *client.Client
	err  error
}

func waitForHost(ctx context.Context, clock Clock, dial func(context.Context) (*client.Client, error)) (*client.Client, error) {
	deadline := clock.NewTimer(10 * time.Second)
	defer deadline.Stop()
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		result, err := awaitDial(child, deadline.C(), dial)
		if err != nil {
			return nil, err
		}
		if result.err == nil {
			return result.conn, nil
		}
		if !errors.Is(result.err, client.ErrHostNotRunning) {
			return nil, fmt.Errorf("host startup: %w", result.err)
		}
		if err = waitStartupRetry(child, clock, deadline.C()); err != nil {
			return nil, err
		}
	}
}

func awaitDial(ctx context.Context, deadline <-chan time.Time, dial func(context.Context) (*client.Client, error)) (dialResult, error) {
	done := make(chan dialResult)
	go func() {
		conn, err := dial(ctx)
		select {
		case done <- dialResult{conn: conn, err: err}:
		case <-ctx.Done():
			if conn != nil {
				conn.Close()
			}
		}
	}()
	select {
	case <-ctx.Done():
		return dialResult{}, fmt.Errorf("host startup canceled: %w", ctx.Err())
	case <-deadline:
		return dialResult{}, ErrStartupTimeout
	case result := <-done:
		return result, nil
	}
}

func waitStartupRetry(ctx context.Context, clock Clock, deadline <-chan time.Time) error {
	retry := clock.NewTimer(100 * time.Millisecond)
	defer retry.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("host startup canceled: %w", ctx.Err())
	case <-deadline:
		return ErrStartupTimeout
	case <-retry.C():
		return nil
	}
}

func hostEnvironment(env []string, dataDir string) []string {
	allowed := map[string]bool{"HOME": true, "PATH": true, "XDG_RUNTIME_DIR": true, "XDG_CONFIG_HOME": true, "TMPDIR": true, "LANG": true, "SYNX_FUGUE_LOG_LEVEL": true}
	var out []string
	for _, pair := range env {
		key, _, _ := strings.Cut(pair, "=")
		if allowed[key] {
			out = append(out, pair)
		}
	}
	return append(out, "SYNX_FUGUE_DATA_DIR="+dataDir)
}
