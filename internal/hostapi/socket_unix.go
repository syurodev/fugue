//go:build unix

package hostapi

import (
	"io/fs"
	"os"
	"syscall"
)

// osDirs opens directories on unix systems: O_DIRECTORY|O_NOFOLLOW fails on a
// symlinked path, and Stat and Chmod act on the open descriptor so nothing can
// replace the directory between the check and the chmod.
type osDirs struct{}

func (osDirs) Open(path string) (Dir, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return osDir{file: f}, nil
}

type osDir struct{ file *os.File }

func (d osDir) Stat() (DirStat, error) {
	fi, err := d.file.Stat()
	if err != nil {
		return DirStat{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return DirStat{Mode: fi.Mode()}, nil
	}
	return DirStat{Mode: fi.Mode(), UID: st.Uid, UIDKnown: true}, nil
}

func (d osDir) Chmod(mode fs.FileMode) error { return d.file.Chmod(mode) }

func (d osDir) Close() error { return d.file.Close() }
