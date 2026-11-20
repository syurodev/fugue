package client

import (
	"fmt"
	"sync"
)

type heavyStream[Res any] interface {
	Receive() bool
	Msg() *Res
	Err() error
	Close() error
}

func waitHeavyGrant[Res any](stream heavyStream[Res], acquired func(*Res) bool) (func(), error) {
	for stream.Receive() {
		if acquired(stream.Msg()) {
			var once sync.Once
			return func() {
				once.Do(func() { closeBestEffort(stream) })
			}, nil
		}
	}
	err := stream.Err()
	closeBestEffort(stream)
	if err == nil {
		return nil, fmt.Errorf("acquire heavy command slot: %w", ErrDisconnected)
	}
	return nil, fmt.Errorf("acquire heavy command slot: %w", err)
}
