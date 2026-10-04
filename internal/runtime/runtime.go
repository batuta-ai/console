package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type Paths struct {
	Home   string
	DB     string
	Auth   string
	Lock   string
	Logs   string
	Listen string
}

func Resolve(home, listen string) (Paths, error) {
	if home == "" {
		home = os.Getenv("BATUTA_CONSOLE_HOME")
	}
	if home == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		home = filepath.Join(dir, ".local", "state", "batuta", "console")
	}
	if listen == "" {
		listen = os.Getenv("BATUTA_CONSOLE_LISTEN")
	}
	if listen == "" {
		listen = "127.0.0.1:9477"
	}
	if err := requireLoopback(listen); err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return Paths{}, err
	}
	dir, err := privateDirectory(home)
	if err != nil {
		return Paths{}, err
	}
	if err := dir.Close(); err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err != nil {
		return Paths{}, err
	}
	return Paths{
		Home:   home,
		DB:     filepath.Join(home, "console.db"),
		Auth:   filepath.Join(home, "auth.secret"),
		Lock:   filepath.Join(home, "consoled.lock"),
		Logs:   filepath.Join(home, "logs"),
		Listen: listen,
	}, nil
}

func requireLoopback(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen must be host:port: %w", err)
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("listen must be loopback, got %s", listen)
}

type LockInfo struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
	Home string `json:"home"`
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

func ReadLock(path string) (LockInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return LockInfo{}, err
	}
	var info LockInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return LockInfo{}, err
	}
	return info, nil
}

func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func ParsePID(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

var _ = io.EOF
