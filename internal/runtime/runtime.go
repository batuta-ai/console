package runtime

import (
	"crypto/rand"
	"encoding/hex"
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

func LoadOrCreateAuth(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		tok := string(b)
		if len(tok) >= 32 {
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw[:])
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("daemon already running: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	enc, err := json.Marshal(info)
	if err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Write(enc); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
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
