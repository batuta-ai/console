package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/batuta-ai/console/integrations/batuta"
)

var nativeSkillsMu sync.Mutex

func prepareSessionSkills(workspace string, enable bool, adapter string, accountEnv, roles []string) (batuta.Profile, error) {
	p := batuta.Prepare(workspace, enable)
	if !enable {
		return p, nil
	}
	selected, err := p.NativeSkills(roles)
	if err != nil {
		return batuta.Profile{}, err
	}
	if !p.Enabled {
		return p, nil
	}
	// Custom commands without an account keep their disposable runtime HOME.
	// There is no selected account discovery directory to prepare in that case.
	if len(accountEnv) == 0 {
		return batuta.Prepare(workspace, false), nil
	}
	if err := installNativeSkills(adapter, accountEnv, selected); err != nil {
		return batuta.Profile{}, fmt.Errorf("prepare native skills: %w", err)
	}
	return p, nil
}

func installNativeSkills(adapter string, env []string, selected map[string]string) error {
	values := map[string]string{}
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	home := values["HOME"]
	configKey, configDir, nativeDir := "CODEX_HOME", ".codex", ".agents"
	if adapter == "claude" {
		configKey, configDir, nativeDir = "CLAUDE_CONFIG_DIR", ".claude", ".claude"
	} else if adapter != "codex" {
		return fmt.Errorf("unsupported skill adapter %q", adapter)
	}
	if !filepath.IsAbs(home) || values[configKey] != filepath.Join(home, configDir) {
		return fmt.Errorf("explicit isolated HOME and %s required", configKey)
	}
	nativeSkillsMu.Lock()
	defer nativeSkillsMu.Unlock()
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("skill HOME must be a directory without symlinks: %s", home)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	// Check the other Codex user discovery location too: it can shadow a skill.
	if adapter == "codex" {
		config, err := openSkillDirectory(root, configDir, false)
		if err != nil {
			return err
		}
		if config != nil {
			defer config.Close()
			other, err := openSkillDirectory(config, "skills", false)
			if err != nil {
				return err
			}
			if other != nil {
				defer other.Close()
				if err := validateSkillLinks(other, selected); err != nil {
					return err
				}
			}
		}
	}
	parent, err := openSkillDirectory(root, nativeDir, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	destination, err := openSkillDirectory(parent, "skills", true)
	if err != nil {
		return err
	}
	defer destination.Close()
	if err := validateSkillLinks(destination, selected); err != nil {
		return err
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := selected[name]
		if err := destination.Symlink(target, name); err != nil {
			if !os.IsExist(err) {
				return err
			}
			if err := validateSkillLink(destination, name, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// Root-relative operations keep writes inside the selected account even if a
// destination is swapped during preparation. Existing directories are not chmodded.
func openSkillDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		if err := parent.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) && !create {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill destination must be a directory without symlinks: %s/%s", parent.Name(), name)
	}
	return parent.OpenRoot(name)
}

func validateSkillLinks(dir *os.Root, selected map[string]string) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// Dot directories are not user skills (Codex owns its .system bundle).
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		target, ok := selected[entry.Name()]
		if !ok {
			return fmt.Errorf("unselected skill destination %s/%s; reconcile it before starting", dir.Name(), entry.Name())
		}
		if err := validateSkillLink(dir, entry.Name(), target); err != nil {
			return err
		}
	}
	return nil
}

func validateSkillLink(dir *os.Root, name, target string) error {
	existing, err := dir.Readlink(name)
	if err != nil || existing != target {
		return fmt.Errorf("conflicting skill destination %s/%s; existing content was preserved", dir.Name(), name)
	}
	return nil
}
