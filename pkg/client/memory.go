package client

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
)

const memoryExportWireMaxBytes = 32*1024*1024 + 64*1024

// ExportMemory exports all workspace memory entries as a fugue-memory/1 Markdown file.
func (c *Client) ExportMemory(ctx context.Context, workspaceID string) ([]byte, error) {
	svc := memoryv1connect.NewMemoryServiceClient(
		c.hc, c.base,
		connect.WithReadMaxBytes(memoryExportWireMaxBytes),
	)
	res, err := svc.ExportMemory(ctx, connect.NewRequest(&memoryv1.ExportMemoryRequest{
		WorkspaceId: workspaceID,
	}))
	if err != nil {
		return nil, fmt.Errorf("export workspace memory: %w", err)
	}
	return res.Msg.GetContent(), nil
}
