package hostconn

import (
	"context"

	"github.com/syurodev/fugue/pkg/client"
)

func Stream(ctx context.Context, c *client.Client) error {
	s, err := c.Subscribe(ctx, "host", nil)
	if err != nil {
		return err
	}
	return s.Close()
}
