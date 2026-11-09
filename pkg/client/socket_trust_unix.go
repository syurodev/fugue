//go:build linux || darwin

package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func dialTrustedSocket(ctx context.Context, path string) (net.Conn, error) {
	if err := validateSocket(path); err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("connect socket: %w", err)
	}
	if err = validatePeer(conn); err != nil {
		closeBestEffort(conn)
		return nil, err
	}
	return conn, nil
}

func validateSocket(path string) error {
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("stat socket directory: %w", err)
	}
	if err = trustPrivate(dir, true); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat socket: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(st.Uid) != int64(os.Getuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("socket ownership or permissions: %w", ErrUntrustedHost)
	}
	return nil
}

func validatePeer(conn net.Conn) error {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return ErrUntrustedHost
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return fmt.Errorf("socket credentials: %w", err)
	}
	var peerErr error
	if err = raw.Control(func(fd uintptr) { peerErr = checkPeer(fd) }); err != nil || peerErr != nil {
		return fmt.Errorf("socket credentials: %w", ErrUntrustedHost)
	}
	return nil
}
