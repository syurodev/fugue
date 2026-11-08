package client

import (
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

type Client struct {
	req connect.Request
	msg proto.Message
}
