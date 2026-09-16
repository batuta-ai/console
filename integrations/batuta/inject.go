package batuta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Profile is the session-scoped Batuta preparation. It never overwrites a
// CLI's global login. Skills stay where the host already installed them.
type Profile struct {
	Enabled       bool     `json:"enabled"`
	SkillsRoot    string   `json:"skills_root,omitempty"`
	SkillsVersion string   `json:"skills_version,omitempty"`
	Env           []string  `json:"env"`
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
	root := skillsRoot()
	if root == "" {
		p.Enabled = false
		p.Notes = append(p.Notes, "no skills directory found; session will run without Batuta injection")
		return p
	}
	p.SkillsRoot = root
	p.SkillsVersion = readLockVersion(root)
	p.Env = append(p.Env, "BATUTA_SKILLS="+root)
	if workspace != "" {
		p.Env = append(p.Env, "BATUTA_JOURNAL="+filepath.Join(workspace, ".batuta", "journal"))
	}
	p.Notes = append(p.Notes, "skills and config apply to this session only; restart the CLI to pick up later changes")
	return p
}

func skillsRoot() string {
	if v, ok := os.LookupEnv("BATUTA_SKILLS"); ok {
		if v == "" {
			return ""
		}
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			return v
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "plugins", "batuta"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
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
