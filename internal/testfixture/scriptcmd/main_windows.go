package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type descriptor struct {
	Executable string `json:"executable"`
	ImageSHA   string `json:"image_sha256"`
	Shell      string `json:"shell"`
	ShellSHA   string `json:"shell_sha256"`
	Script     string `json:"script"`
	ScriptSHA  string `json:"script_sha256"`
}

func checkDigest(path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("fixture source changed: %s", path)
	}
	return nil
}

func run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(executable + ".shell-config.json")
	if err != nil {
		return err
	}
	if len(data) > 64<<10 {
		return errors.New("fixture descriptor too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var metadata descriptor
	if err := decoder.Decode(&metadata); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("fixture descriptor has trailing content")
	}
	if !strings.EqualFold(metadata.Executable, executable) || metadata.Script != metadata.Executable+".shell-script" || !filepath.IsAbs(metadata.Shell) {
		return errors.New("fixture descriptor path mismatch")
	}
	for _, source := range []struct{ path, digest string }{{executable, metadata.ImageSHA}, {metadata.Shell, metadata.ShellSHA}, {metadata.Script, metadata.ScriptSHA}} {
		if err := checkDigest(source.path, source.digest); err != nil {
			return err
		}
	}
	bootstrap := `fixture_script=$1; fixture_home=$2; shift 2; HOME=$fixture_home; export HOME; . "$fixture_script"`
	args := append([]string{"--noprofile", "--norc", "-p", "-c", bootstrap, metadata.Executable, metadata.Script, os.Getenv("HOME")}, os.Args[1:]...)
	command := exec.Command(metadata.Shell, args...)
	// MSYS recognizes apostrophe quoting in its initial command line as well.
	quoted := make([]string, 0, len(args)+1)
	for _, arg := range append([]string{metadata.Shell}, args...) {
		quoted = append(quoted, quoteMSYSArgument(arg))
	}
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: strings.Join(quoted, " ")}
	command.Env = append(os.Environ(), "MSYS2_ARG_CONV_EXCL=*")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

func quoteMSYSArgument(arg string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	slashes := 0
	for _, character := range arg {
		if character == '\\' {
			slashes++
			continue
		}
		if character == '"' {
			quoted.WriteString(strings.Repeat("\\", 2*slashes+1))
		} else {
			quoted.WriteString(strings.Repeat("\\", slashes))
		}
		quoted.WriteRune(character)
		slashes = 0
	}
	quoted.WriteString(strings.Repeat("\\", 2*slashes))
	quoted.WriteByte('"')
	return quoted.String()
}

func main() {
	if err := run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
}
