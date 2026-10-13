package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareRuntimeHome(home string) error {
	if err := EnsurePrivateDirectory(home); err != nil {
		return err
	}
	return EnsurePrivateDirectory(filepath.Join(home, "logs"))
}

func defaultRuntimeHome() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Batuta", "Console"), nil
}

func LoadOrCreateAuth(path string) (token string, err error) {
	dir, err := OpenPrivateDirectory(filepath.Dir(path))
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
	lock, err := windowsPrivateFileAtMode(dir, name+".lock", windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF, false)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
		if err != nil {
			token = ""
		}
	}()
	if err := windowsLockFile(lock, false); err != nil {
		return "", err
	}
	f, err := windowsPrivateFileAt(dir, name, windows.FILE_GENERIC_READ, windows.FILE_OPEN)
	if err == nil {
		defer func() {
			err = errors.Join(err, f.Close())
			if err != nil {
				token = ""
			}
		}()
		data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		if err != nil {
			return "", err
		}
		if len(data) < 32 || len(data) > 64<<10 {
			return "", fmt.Errorf("existing auth secret must be complete and bounded; recover the owned file explicitly")
		}
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return createWindowsAuthAt(dir, name)
}

func createWindowsAuthAt(dir *os.File, name string) (token string, err error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	staging := "." + name + "." + hex.EncodeToString(suffix[:]) + ".tmp"
	f, err := windowsPrivateFileAt(dir, staging, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, windows.FILE_CREATE)
	if err != nil {
		return "", err
	}
	published := false
	defer func() {
		if !published {
			remove := byte(1)
			err = errors.Join(err, windows.NtSetInformationFile(windows.Handle(f.Fd()), &windows.IO_STATUS_BLOCK{}, &remove, 1, windows.FileDispositionInformation))
		}
		err = errors.Join(err, f.Close())
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
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return "", err
	}
	type renameInformation struct {
		ReplaceIfExists uint32
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [1]uint16
	}
	var layout renameInformation
	length := uint32((len(encoded) - 1) * 2)
	offset := unsafe.Offsetof(layout.FileName)
	buffer := make([]byte, int(offset)+int(length))
	info := (*renameInformation)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = windows.Handle(dir.Fd())
	info.FileNameLength = length
	copy(unsafe.Slice(&info.FileName[0], len(encoded)-1), encoded[:len(encoded)-1])
	// ReplaceIfExists remains false: complete staging bytes never replace a token.
	if err := windows.NtSetInformationFile(windows.Handle(f.Fd()), &windows.IO_STATUS_BLOCK{}, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation); err != nil {
		return "", err
	}
	published = true
	return token, nil
}

func WriteLock(path string, info LockInfo) (owned *os.File, err error) {
	encoded, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	if len(encoded) > windowsMaxLockBytes {
		return nil, fmt.Errorf("daemon lock metadata exceeds the bounded size")
	}
	dir, err := OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, dir.Close())
		if err != nil && owned != nil {
			err = errors.Join(err, owned.Close())
			owned = nil
		}
	}()
	f, err := windowsPrivateFileAt(dir, filepath.Base(path), windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF)
	if err != nil {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
		return nil, err
	}
	if err := windowsLockFile(f, true); err != nil {
		return nil, errors.Join(fmt.Errorf("daemon already running: %w", err), f.Close())
	}
	if err := writeWindowsLockMetadata(dir, f, filepath.Base(path), encoded); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}
