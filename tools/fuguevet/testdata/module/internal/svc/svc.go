package svc

import (
	"log/slog"

	"google.golang.org/protobuf/proto"
)

/* TODO: write this later */

func Run(token string, msg proto.Message) {
	go func() {}()
	slog.Info("login", "token", token)
	_ = msg
}
