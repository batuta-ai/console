package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RuntimeEnvironment closes inheritance at the final execution boundary.
// Explicit identity/metadata is supplied by Console, never the daemon's env.
func RuntimeEnvironment(ambient, explicit []string) []string {
	runtimeKeys := map[string]bool{
		"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "LC_COLLATE": true, "LC_MESSAGES": true, "LC_MONETARY": true, "LC_NUMERIC": true, "LC_TIME": true, "LC_ADDRESS": true, "LC_IDENTIFICATION": true, "LC_MEASUREMENT": true, "LC_NAME": true, "LC_PAPER": true, "LC_TELEPHONE": true,
		"TERM": true, "COLORTERM": true, "SHELL": true, "DISPLAY": true, "WAYLAND_DISPLAY": true, "XDG_RUNTIME_DIR": true, "DBUS_SESSION_BUS_ADDRESS": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "CODEX_CA_CERTIFICATE": true,
	}
	controlledKeys := map[string]bool{
		"HOME": true, "TMPDIR": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true, "CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true, "CURSOR_CONFIG_DIR": true,
		"BATUTA_CONSOLE": true, "BATUTA_WORKSPACE": true, "BATUTA_SKILLS": true, "BATUTA_JOURNAL": true, "BATUTA_ROLE_SKILLS": true, "BATUTA_MISSION": true, "BATUTA_INIT": true,
	}
	values := map[string]string{}
	for _, entry := range ambient {
		key, value, ok := strings.Cut(entry, "=")
		if ok && runtimeKeys[key] {
			values[key] = value
		}
	}
	for _, entry := range explicit {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (runtimeKeys[key] || controlledKeys[key]) {
			values[key] = value
		}
	}
	values["TERM"] = "xterm-256color"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func ExplicitHome(env []string) string {
	home := ""
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "HOME" {
			home = value
		}
	}
	return home
}

// SessionEnvironment owns a new credentialless home. The caller removes only
// this returned directory after its complete process/reader lifecycle.
func SessionEnvironment() (env []string, home string, err error) {
	home, err = os.MkdirTemp("", "batuta-console-session-")
	if err != nil {
		return nil, "", err
	}
	env, err = prepareSessionEnvironment(home)
	if err != nil {
		return nil, "", err
	}
	return env, home, nil
}

// home must be the newly owned directory created by SessionEnvironment.
func prepareSessionEnvironment(home string) (env []string, err error) {
	paths := map[string]string{
		"HOME": home, "TMPDIR": filepath.Join(home, "tmp"), "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"CODEX_HOME": filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "CURSOR_CONFIG_DIR": filepath.Join(home, ".cursor"),
	}
	for key, path := range paths {
		if err = os.MkdirAll(path, 0700); err != nil {
			return nil, errors.Join(err, os.RemoveAll(home))
		}
		env = append(env, key+"="+path)
	}
	return env, nil
}
