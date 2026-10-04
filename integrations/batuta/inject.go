package batuta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/loop"
)

// Profile is the session-scoped Batuta preparation. It never overwrites a
// CLI's global login. Skills stay where the host already installed them.
type Profile struct {
	Enabled       bool     `json:"enabled"`
	SkillsRoot    string   `json:"skills_root,omitempty"`
	SkillsVersion string   `json:"skills_version,omitempty"`
	Env           []string `json:"env"`
	Notes         []string `json:"notes"`
}

// Prepare builds environment for one Console session. Global CLI config and
// credentials are left untouched. If Batuta skills are not on disk, the
// session still starts as a generic CLI (B2).
func Prepare(workspace string, enable bool) Profile {
	p := Profile{Enabled: enable, Env: []string{
		"BATUTA_CONSOLE=1",
		"BATUTA_WORKSPACE=" + workspace,
	}}
	if !enable {
		p.Notes = append(p.Notes, "Batuta injection off; generic CLI session")
		return p
	}
	root := resolveSkills(workspace)
	if root == "" {
		p.Enabled = false
		p.Notes = append(p.Notes, "no skills directory found; session will run without Batuta injection")
		return p
	}
	p.SkillsRoot = root
	p.SkillsVersion = readLockVersion(root)
	if p.SkillsVersion == "present" {
		p.SkillsVersion = readLockVersion(filepath.Dir(root))
	}
	if p.SkillsVersion == "present" && filepath.Base(filepath.Dir(root)) == "skills" {
		p.SkillsVersion = readLockVersion(filepath.Dir(filepath.Dir(root)))
	}
	p.Env = append(p.Env, "BATUTA_SKILLS="+root)
	if workspace != "" {
		p.Env = append(p.Env, "BATUTA_JOURNAL="+filepath.Join(workspace, ".batuta", "journal"))
	}
	p.Notes = append(p.Notes, "skill sources are validated for this launch; restart the CLI to pick up later changes")
	return p
}

func skillsRoot() string {
	workspace, err := os.Getwd()
	if err != nil {
		return ""
	}
	return resolveSkills(workspace)
}

func resolveSkills(workspace string) string {
	if configured, ok := os.LookupEnv("BATUTA_SKILLS"); ok {
		if strings.TrimSpace(configured) == "" {
			return ""
		}
		for _, candidate := range []string{configured, filepath.Join(configured, "batuta"), filepath.Join(configured, "skills", "batuta")} {
			if root, err := loop.FindSkills(workspace, candidate); err == nil && validSkill(root) {
				return root
			}
		}
		return ""
	}
	root, err := loop.FindSkills(workspace, "")
	if err != nil || !validSkill(root) {
		return ""
	}
	return root
}

func validSkill(path string) bool {
	info, err := os.Stat(filepath.Join(path, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}

func readLockVersion(root string) string {
	for _, name := range []string{".batuta-skills-lock.json", "skills-lock.json"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		for _, k := range []string{"version", "ref", "tag"} {
			if s, ok := raw[k].(string); ok && s != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return "present"
}
