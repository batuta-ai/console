//go:build darwin || linux

package service

import "syscall"

func execNativeBridge(executable string, args, env []string) error {
	return syscall.Exec(executable, append([]string{executable}, args...), env)
}
