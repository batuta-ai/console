//go:build !windows

package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Every Console token reader holds the same persistent initialization lock.
// A partial file left by interrupted creation is refused, never rotated.
func LoadOrCreateAuth(path string) (token string, err error) {
	dir, err := privateDirectory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, dir.Close())
		if err != nil {
			token = ""
		}
	}()
	name := filepath.Base(path)
	lock, err := privateFileAt(dir, name+".lock", unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
		if err != nil {
			token = ""
		}
	}()
	if err := flock(lock, unix.LOCK_EX); err != nil {
		return "", err
	}

	f, err := privateFileAt(dir, name, unix.O_RDONLY)
	if err == nil {
		defer func() {
			err = errors.Join(err, f.Close())
			if err != nil {
				token = ""
			}
		}()
		data, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		if len(data) < 32 {
			return "", fmt.Errorf("existing auth secret is incomplete; recover the owned file explicitly")
		}
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return createAuthAt(dir, name)
}

func createAuthAt(dir *os.File, name string) (token string, err error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	staging := "." + name + "." + hex.EncodeToString(suffix[:]) + ".tmp"
	f, err := privateFileAt(dir, staging, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, f.Close(), unix.Unlinkat(int(dir.Fd()), staging, 0))
		if err != nil {
			token = ""
		}
	}()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw[:])
	if _, err := f.WriteString(token); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	// Publish complete bytes without replacing any existing final file.
	if err := unix.Linkat(int(dir.Fd()), staging, int(dir.Fd()), name, 0); err != nil {
		return "", &os.PathError{Op: "publish auth secret", Path: filepath.Join(dir.Name(), name), Err: err}
	}
	return token, nil
}

func privateDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private directory", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	err = unix.Fstat(fd, &info)
	if err == nil && (info.Uid != uint32(os.Geteuid()) || info.Mode&0022 != 0) {
		err = fmt.Errorf("runtime directory must be owned by the current user without group/other write permission")
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func privateFileAt(dir *os.File, name string, flags int) (*os.File, error) {
	path := filepath.Join(dir.Name(), name)
	fd, err := openRuntimeFileAt(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC)
	if err != nil {
		return nil, &os.PathError{Op: "open private file", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	var info unix.Stat_t
	err = unix.Fstat(fd, &info)
	if err == nil && (info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) || info.Nlink != 1) {
		err = fmt.Errorf("runtime file must be a regular single-linked file owned by the current user")
	}
	if err == nil && info.Mode&07777 != 0600 {
		err = f.Chmod(0600)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func flock(f *os.File, mode int) error {
	for {
		err := unix.Flock(int(f.Fd()), mode)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
