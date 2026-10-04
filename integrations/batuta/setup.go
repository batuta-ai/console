package batuta

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

const SetupFileLimit = 1 << 20

// SetupConfiguration is a disk observation; reading it never executes commands.
type SetupConfiguration struct {
	Ready bool   `json:"ready"`
	Code  string `json:"code,omitempty"`
}

func ValidateSetupRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("invalid_workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid_workspace: root must be an existing directory")
	}
	return nil
}

func CompleteProfile(p loop.Profile) bool {
	return p.Stack != "" && p.Methodology != "" && p.Test != "" && p.Build != "" && p.Install != "" && p.Template != "" && (p.Execution == "sequential" || p.Execution == "parallel") && (p.Worktree == "off" || p.Worktree == "medium+" || p.Worktree == "always")
}

func ReadSetup(root string) SetupConfiguration {
	if err := ValidateSetupRoot(root); err != nil {
		return SetupConfiguration{Code: "invalid_workspace"}
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return SetupConfiguration{Code: "invalid_workspace"}
	}
	defer dir.Close()
	paths := []string{".batuta/profile.md", ".batuta/routing.md", "WORK.md"}
	data := make([][]byte, len(paths))
	for i, path := range paths {
		payload, err := readSetupFile(dir, path)
		if err != nil {
			if errors.Is(err, errors.ErrUnsupported) {
				return SetupConfiguration{Code: "setup_unsupported_platform"}
			}
			code := []string{"profile_unreadable", "routing_unreadable", "work_unreadable"}[i]
			if os.IsNotExist(err) {
				code = []string{"profile_missing", "routing_missing", "work_missing"}[i]
			}
			return SetupConfiguration{Code: code}
		}
		data[i] = payload
	}
	if !CompleteProfile(loop.ParseProfile(string(data[0]))) {
		return SetupConfiguration{Code: "profile_incomplete"}
	}
	table, err := routing.ParseRoutingTable(data[1])
	if err != nil {
		return SetupConfiguration{Code: "routing_invalid"}
	}
	lanes := map[routing.Complexity]bool{}
	for _, row := range table.Rows {
		if row.Executor != "" && strings.TrimSpace(row.Model) != "" {
			lanes[row.Lane] = true
		}
	}
	for _, lane := range []routing.Complexity{routing.ComplexityLow, routing.ComplexityMedium, routing.ComplexityHigh, routing.ComplexityCritical} {
		if !lanes[lane] {
			return SetupConfiguration{Code: "routing_incomplete"}
		}
	}
	heading, progress, done := false, false, false
	for _, line := range strings.Split(string(data[2]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "# WORK — ") && strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(line), "# WORK — ")) != "" {
			heading = true
		}
		if line == "## In progress" {
			progress = true
		}
		if line == "## Done" {
			done = true
		}
	}
	if !heading || !progress || !done {
		return SetupConfiguration{Code: "work_incomplete"}
	}
	return SetupConfiguration{Ready: true}
}

func readSetupFile(root *os.Root, path string) ([]byte, error) {
	if strings.HasPrefix(path, ".batuta/") {
		info, err := root.Lstat(".batuta")
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf(".batuta must be a directory")
		}
	}
	f, err := openSetupFile(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > SetupFileLimit {
		return nil, fmt.Errorf("setup file must be regular and at most %d bytes", SetupFileLimit)
	}
	payload, err := io.ReadAll(io.LimitReader(f, SetupFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > SetupFileLimit {
		return nil, fmt.Errorf("setup file exceeds byte limit")
	}
	return payload, nil
}
