//go:build linux

package client

import (
	"math"
	"os"

	"golang.org/x/sys/unix"
)

func checkPeer(fd uintptr) error {
	if fd > uintptr(math.MaxInt) {
		return ErrUntrustedHost
	}
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int64(cred.Uid) != int64(os.Getuid()) {
		return ErrUntrustedHost
	}
	return nil
}
