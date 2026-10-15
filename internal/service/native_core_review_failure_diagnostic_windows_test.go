package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

func task43CoreReviewFailureDiagnostic(t *testing.T, a domain.PlanApproval, home string) {
	// Registered after fixture cleanup, so LIFO preserves this test's evidence.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: batuta.ExecutionDelivery(a)}
		observation, err := loop.ObserveSupervision(opts)
		t.Logf("same-test review diagnostic workspace=%q delivery=%q observation_error=%v", opts.Workspace, opts.Delivery, err)
		task43CoreReviewDiagnosticValue(t, "observation", observation)
		task43CoreReviewDiagnosticValue(t, "review_job", observation.Review)
		for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json", "review_failures.json"} {
			if job := observation.Review; job != nil && job.Artifacts != "" && job.Delivery == opts.Delivery {
				task43CoreReviewDiagnosticFile(t, opts.Workspace, filepath.Join(job.Artifacts, name), job.ArtifactDigests[name])
			} else {
				t.Logf("same-test review diagnostic artifact=%q read_error=own reported job/artifacts unavailable recorded_sha256=unavailable computed_sha256=unavailable", name)
			}
		}
		task43CoreReviewDiagnosticFile(t, opts.Workspace, filepath.Join(opts.Workspace, ".batuta", "journal", opts.Delivery+".jsonl"), "")
		for _, name := range []string{"native-launches", "native-reviews", "native-effort-argv"} {
			task43CoreReviewDiagnosticFile(t, home, filepath.Join(home, name), "")
		}
	})
}

func task43CoreReviewDiagnosticValue(t *testing.T, name string, value interface{}) {
	data, err := json.Marshal(value)
	const limit = 64 << 10
	size := len(data)
	if len(data) > limit {
		data = data[:limit]
	}
	t.Logf("same-test review diagnostic %s bytes=%d captured=%d truncated=%t marshal_error=%v\n%s", name, size, len(data), size > len(data), err, data)
}

func task43CoreReviewDiagnosticFile(t *testing.T, owned, path, recorded string) {
	const limit = 256 << 10
	// Resolve links before reading: only this fixture's workspace or selected HOME.
	root, rootErr := filepath.EvalSymlinks(owned)
	resolved, err := filepath.EvalSymlinks(path)
	if rootErr != nil || err != nil {
		t.Logf("same-test review diagnostic path=%q recorded_sha256=%q computed_sha256=unavailable ownership_error=%v read_error=%v", path, recorded, rootErr, err)
		return
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Logf("same-test review diagnostic path=%q recorded_sha256=%q computed_sha256=unavailable read_error=outside owned fixture (%v)", path, recorded, err)
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		t.Logf("same-test review diagnostic path=%q recorded_sha256=%q computed_sha256=unavailable read_error=%v", path, recorded, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		t.Logf("same-test review diagnostic path=%q recorded_sha256=%q computed_sha256=unavailable read_error=not readable regular file (%v)", path, recorded, err)
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	truncated := len(data) > limit || info.Size() > int64(len(data))
	if len(data) > limit {
		data = data[:limit]
	}
	digest := "unavailable (incomplete read)"
	if !truncated && readErr == nil {
		digest = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	t.Logf("same-test review diagnostic path=%q bytes=%d captured=%d truncated=%t recorded_sha256=%q computed_sha256=%q read_error=%v\n%s", path, info.Size(), len(data), truncated, recorded, digest, readErr, data)
}
