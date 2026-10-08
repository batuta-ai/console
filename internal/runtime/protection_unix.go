//go:build !windows

package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func prepareRuntimeHome(home string) error {
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	dir, err := privateDirectory(home)
	if err != nil {
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err != nil {
		return err
	}
	return nil
}

func WriteLock(path string, info LockInfo) (*os.File, error) {
	dir, err := privateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	f, err := privateFileAt(dir, filepath.Base(path), syscall.O_CREAT|syscall.O_RDWR)
	err = errors.Join(err, dir.Close())
	if err != nil {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
		return nil, err
	}
	if err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("daemon already running: %w", err), f.Close())
	}
	if err := f.Truncate(0); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	enc, err := json.Marshal(info)
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if _, err := f.Write(enc); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}
