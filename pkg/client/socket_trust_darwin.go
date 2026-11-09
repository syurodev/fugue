//go:build darwin

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
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil || int64(cred.Uid) != int64(os.Getuid()) {
		return ErrUntrustedHost
	}
	return nil
}
