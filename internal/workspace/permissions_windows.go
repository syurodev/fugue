//go:build windows

package workspace

import "os"

func fileSysUID(info os.FileInfo) (int, error) {
	return os.Getuid(), nil
}
