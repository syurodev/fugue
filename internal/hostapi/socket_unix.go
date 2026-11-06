//go:build unix

package hostapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// osDirs opens directories on unix systems: the final path component is
// opened through an os.Root with O_DIRECTORY|O_NOFOLLOW, so a symlinked or
// swapped directory fails the open, and Stat and Chmod act on that open
// descriptor.
type osDirs struct{}

func (osDirs) Open(path string) (Dir, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "open parent of socket directory "+path, err)
	}
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	closeQuietly(root)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "open socket directory "+path, err)
	}
	return osDir{file: f}, nil
}

type osDir struct{ file *os.File }

func (d osDir) Stat() (DirStat, error) {
	fi, err := d.file.Stat()
	if err != nil {
		return DirStat{}, errcode.Wrap(errcode.Internal, "stat socket directory descriptor", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return DirStat{Mode: fi.Mode()}, nil
	}
	return DirStat{Mode: fi.Mode(), UID: int64(st.Uid), UIDKnown: true}, nil
}

func (d osDir) Chmod(mode fs.FileMode) error {
	if err := d.file.Chmod(mode); err != nil {
		return errcode.Wrap(errcode.Internal, "chmod socket directory descriptor", err)
	}
	return nil
}

func (d osDir) Close() error {
	if err := d.file.Close(); err != nil {
		return errcode.Wrap(errcode.Internal, "close socket directory descriptor", err)
	}
	return nil
}
