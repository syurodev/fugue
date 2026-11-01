package leaky

import (
	"connectrpc.com/connect"           // want `import "connectrpc.com/connect": proto-generated and Connect code stays inside internal/hostapi`
	"google.golang.org/protobuf/proto" // want `import "google.golang.org/protobuf/proto": proto-generated`
)

type Handler struct {
	req connect.Request
	msg proto.Message
}
