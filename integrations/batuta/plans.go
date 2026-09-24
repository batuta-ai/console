package batuta

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
)

var planStatus = regexp.MustCompile(`(?m)^\*\*Created:\*\*.*\*\*Status:\*\*\s*(proposed|approved|in progress|done)\s*$`)

// ListPlans reads Batuta plan files under .batuta/plans. Missing directories are an empty list.
func ListPlans(root string) []domain.PlanRef {
	out := []domain.PlanRef{}
	if root == "" {
		return out
	}
	read := func(dir string, archived bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			status := ""
			if m := planStatus.FindStringSubmatch(string(body)); len(m) == 2 {
				status = m[1]
			}
			out = append(out, domain.PlanRef{
				Name:   strings.TrimSuffix(entry.Name(), ".md"),
				Status: status,
				Done:   archived || status == "done",
			})
		}
	}
	base := filepath.Join(root, ".batuta", "plans")
	read(base, false)
	read(filepath.Join(base, "done"), true)
	return out
}
