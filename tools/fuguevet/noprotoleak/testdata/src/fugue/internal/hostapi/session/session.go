package session

import (
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	req connect.Request
	msg proto.Message
}
