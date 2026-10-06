package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
)

// NativeRunner preserves Core's command/result contract while closing credential
// inheritance and sharing the daemon's admission and owned-process drain.
type NativeRunner struct {
	Manager     *Manager
	Environment []string
	Graceful    bool
}

var ErrNativeCleanupUnresolved = errors.New("native command cleanup unresolved")

// CloseAdmission freezes starts immediately without interrupting an owned
// command's cleanup. Shutdown performs termination and joins admitted work.
func (m *Manager) CloseAdmission() { m.mu.Lock(); m.closing = true; m.mu.Unlock() }

func (r NativeRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	failed := publication.CommandResult{ExitCode: -1}
	if r.Manager == nil || !filepath.IsAbs(command.Executable) || command.StdoutLimit < 0 || command.StderrLimit < 0 || command.StdoutLimit > 16<<20 || command.StderrLimit > 16<<20 {
		return failed, errors.New("invalid native command or manager")
	}
	if err := ctx.Err(); err != nil {
		return failed, err
	}
	return r.Manager.runNativeCommand(ctx, command, r.Environment, r.Graceful)
}

// ReserveNative atomically reserves all selected accounts for one delivery.
// The lease lasts through native finalization; parallel subprocesses cannot
// bypass the same capacity observed by interactive sessions.
func (m *Manager) ReserveNative(capacities map[string]int) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrProbeClosing
	}
	if len(capacities) == 0 {
		return nil, errors.New("native execution requires selected accounts")
	}
	for id, capacity := range capacities {
		if id == "" {
			return nil, errors.New("native execution requires an account ID")
		}
		if capacity > 0 && m.accounts[id] >= capacity {
			return nil, fmt.Errorf("account %s at session capacity %d", id, capacity)
		}
	}
	for id := range capacities {
		m.accounts[id]++
	}
	m.active.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			for id := range capacities {
				m.release(id)
			}
			m.active.Done()
		})
	}, nil
}

func NativeEnvironment(explicit, command []string) ([]string, error) {
	// The selected account wins over command-local identity overrides.
	combined := append(append([]string{}, command...), explicit...)
	env := accounts.RuntimeEnvironment(os.Environ(), combined)
	git := map[string]string{}
	for _, entry := range command {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, '\x00') {
			return nil, errors.New("invalid native command environment")
		}
		if strings.HasPrefix(key, "GIT_") {
			git[key] = value
		}
	}
	if value, ok := git["GIT_CONFIG_COUNT"]; ok {
		count, err := strconv.Atoi(value)
		if err != nil || count < 0 || count > 256 {
			return nil, errors.New("invalid native Git configuration")
		}
		for i := 0; i < count; i++ {
			for _, prefix := range []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
				if _, ok := git[prefix+strconv.Itoa(i)]; !ok {
					return nil, errors.New("native Git configuration depends on inherited entries")
				}
			}
		}
	}
	for key, value := range git {
		env = append(env, key+"="+value)
	}
	return env, nil
}
