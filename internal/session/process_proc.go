package session

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func inspectProc(root string, pid int) (ProcessObservation, error) {
	if pid <= 0 {
		return ProcessObservation{}, fmt.Errorf("invalid process PID %d", pid)
	}
	stat, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if os.IsNotExist(err) {
		// hidepid can make an existing foreign PID look like ENOENT.
		if err := procAbsenceObservable(root); err != nil {
			return ProcessObservation{}, err
		}
		return ProcessObservation{Missing: true}, nil
	}
	if err != nil {
		return ProcessObservation{}, fmt.Errorf("inspect PID %d: %w", pid, err)
	}
	boot, bootErr := os.ReadFile(filepath.Join(root, "sys/kernel/random/boot_id"))
	observation, err := parseProcIdentity(pid, stat, boot)
	if observation.Missing {
		return observation, nil
	}
	if bootErr != nil {
		return ProcessObservation{}, fmt.Errorf("read process boot identity: %w", bootErr)
	}
	return observation, err
}

func parseProcIdentity(pid int, stat, boot []byte) (ProcessObservation, error) {
	invalid := func() (ProcessObservation, error) {
		return ProcessObservation{}, fmt.Errorf("invalid proc birth record for PID %d", pid)
	}
	text := string(stat)
	open, close := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 1 || close < open || close+1 >= len(text) {
		return invalid()
	}
	recordPID, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	if err != nil || recordPID != pid {
		return invalid()
	}
	fields := strings.Fields(text[close+1:])
	// comm (field 2) can contain spaces and parentheses; starttime is field 22.
	if len(fields) < 20 || len(fields[0]) != 1 || !strings.Contains("RSDZTtXxKWPI", fields[0]) {
		return invalid()
	}
	if strings.Contains("ZXx", fields[0]) {
		return ProcessObservation{Missing: true}, nil
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return invalid()
	}
	bootID := strings.TrimSpace(string(boot))
	if len(bootID) != 36 || bootID[8] != '-' || bootID[13] != '-' || bootID[18] != '-' || bootID[23] != '-' {
		return invalid()
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(bootID, "-", ""))
	if err != nil || len(raw) != 16 {
		return invalid()
	}
	return ProcessObservation{Identity: fmt.Sprintf("linux:%s:%d:%d", strings.ToLower(bootID), pid, ticks)}, nil
}

func procAbsenceObservable(root string) error {
	mounts, err := os.ReadFile(filepath.Join(root, "mounts"))
	if err != nil {
		return fmt.Errorf("read proc visibility: %w", err)
	}
	found := false
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "/proc" {
			continue
		}
		if found || len(fields) != 6 || fields[2] != "proc" {
			return fmt.Errorf("unverified proc mount")
		}
		found = true
		for _, option := range strings.Split(fields[3], ",") {
			if strings.HasPrefix(option, "hidepid=") && option != "hidepid=0" && option != "hidepid=off" {
				return fmt.Errorf("proc visibility restricted by %s", option)
			}
		}
	}
	if !found {
		return fmt.Errorf("proc mount unavailable for absence observation")
	}
	return nil
}
