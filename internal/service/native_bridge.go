package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
)

type nativeBridgeAccount struct {
	Executable  string           `json:"executable"`
	Environment []string         `json:"environment"`
	Adapter     executor.Adapter `json:"adapter"`
	Members     []domain.Member  `json:"members"`
}

func (s *Service) nativeReviewEnvironment(home string, env []string, selected nativeAccounts) ([]string, error) {
	helper := s.NativeExecutorExecutable
	if helper == "" {
		var err error
		helper, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	helper, err := exec.LookPath(helper)
	if err != nil || !filepath.IsAbs(helper) {
		return nil, domain.ErrExecutionUnavailable
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		return nil, err
	}
	config := map[string]nativeBridgeAccount{}
	for _, cli := range selected.sortedCLIs() {
		account := selected.byCLI[cli]
		config[cli] = nativeBridgeAccount{Executable: account.executable, Environment: account.env, Adapter: account.adapter, Members: account.members}
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > 1<<20 {
		return nil, errors.Join(domain.ErrInvalidReference, err)
	}
	path := filepath.Join(home, "native-review.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return nil, err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	for _, cli := range selected.sortedCLIs() {
		body := "#!/bin/sh\nexec " + quote(helper) + " --native-executor " + quote(path) + " " + quote(cli) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, cli), []byte(body), 0500); err != nil {
			return nil, err
		}
	}
	return append(append([]string{}, env...), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH")), nil
}

// The bridge replaces itself with the selected CLI, preserving the review
// engine's owned worker group and finite stdin. It never starts another daemon.
func RunNativeExecutor(configPath, cli string, args []string) error {
	if !filepath.IsAbs(configPath) || (cli != "codex" && cli != "claude") || len(args) == 0 {
		return domain.ErrInvalidReference
	}
	root, err := os.OpenRoot(filepath.Dir(configPath))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(configPath)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return domain.ErrInvalidReference
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err := errors.Join(readErr, file.Close()); err != nil || len(data) > 1<<20 {
		return errors.Join(domain.ErrInvalidReference, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config map[string]nativeBridgeAccount
	if err := decoder.Decode(&config); err != nil || len(config) == 0 || len(config) > 2 {
		return domain.ErrInvalidReference
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return domain.ErrInvalidReference
	}
	entry, ok := config[cli]
	if !ok || !filepath.IsAbs(entry.Executable) || !filepath.IsAbs(accounts.ExplicitHome(entry.Environment)) || len(entry.Members) == 0 {
		return domain.ErrInvalidReference
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	account := nativeAccount{executable: entry.Executable, env: entry.Environment, adapter: entry.Adapter, members: entry.Members}
	if err := account.authorizeReadonlyCommand(publication.Command{Executable: entry.Executable, Directory: cwd, Args: args}); err != nil {
		return err
	}
	env, err := session.NativeEnvironment(entry.Environment, os.Environ())
	if err != nil {
		return err
	}
	return execNativeBridge(entry.Executable, args, env)
}
