package runtime

import (
	"encoding/json"
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
		dir, err := defaultRuntimeHome()
		if err != nil {
			return Paths{}, err
		}
		home = dir
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
	if err := prepareRuntimeHome(home); err != nil {
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

func ReadLock(path string) (LockInfo, error) {
	b, err := readLockBytes(path)
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
