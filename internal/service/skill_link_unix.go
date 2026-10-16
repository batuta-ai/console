//go:build !windows

package service

import "os"

func validateSkillDirectoryTargets(map[string]string) error { return nil }

func createSkillDirectoryLink(destination *os.Root, target, name string) error {
	return destination.Symlink(target, name)
}
