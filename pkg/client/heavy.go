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

// HeavyLease holds an acquired §7 heavy-command semaphore slot until Release
// is called or the host closes or drops the underlying stream.
//
// Callers drive Wait from their own goroutine to read the stream until EOF or
// error and detect when the lease is lost; Release closes the stream
// idempotently and unblocks Wait.
type HeavyLease struct {
	wait    func() error
	release func()
}

// Wait drains the lease stream until it ends or fails, returning ErrDisconnected
// on a clean stream end or a wrapped stream error otherwise.
func (l *HeavyLease) Wait() error {
	if l == nil || l.wait == nil {
		return nil
	}
	return l.wait()
}

// Release closes the lease stream idempotently, releasing the heavy-command
// semaphore slot on the host.
func (l *HeavyLease) Release() {
	if l == nil || l.release == nil {
		return
	}
	l.release()
}

func waitHeavyGrant[Res any](stream heavyStream[Res], acquired func(*Res) bool) (*HeavyLease, error) {
	for stream.Receive() {
		if acquired(stream.Msg()) {
			var once sync.Once
			release := func() {
				once.Do(func() { closeBestEffort(stream) })
			}
			wait := func() error {
				for stream.Receive() {
				}
				err := stream.Err()
				release()
				if err == nil {
					return fmt.Errorf("heavy command slot lost: %w", ErrDisconnected)
				}
				return fmt.Errorf("heavy command slot lost: %w", err)
			}
			return &HeavyLease{wait: wait, release: release}, nil
		}
	}
	err := stream.Err()
	closeBestEffort(stream)
	if err == nil {
		return nil, fmt.Errorf("acquire heavy command slot: %w", ErrDisconnected)
	}
	return nil, fmt.Errorf("acquire heavy command slot: %w", err)
}
