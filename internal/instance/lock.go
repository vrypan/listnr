// Package instance prevents a daemon and a local import from using the same
// data directory at the same time.
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var ErrLocked = errors.New("listnr data directory is in use")

type Lock struct {
	file *os.File
}

func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, ".listnr.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dataDir)
		}
		return nil, err
	}
	if err := chownLikeDirectory(f, dataDir); err != nil {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
		return nil, err
	}
	return &Lock{file: f}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func chownLikeDirectory(f *os.File, dataDir string) error {
	info, err := os.Stat(dataDir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := f.Chown(int(stat.Uid), int(stat.Gid)); err != nil && !errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}
