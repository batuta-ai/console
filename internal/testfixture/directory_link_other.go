//go:build !windows

package testfixture

import "os"

func DirectoryLink(target, link string) error { return os.Symlink(target, link) }
