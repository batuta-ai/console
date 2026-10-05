package batuta

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

var (
	ErrArtifactMissing = errors.New("bound artifact is missing")
	ErrArtifactInvalid = errors.New("artifact identity or format is invalid")
	ErrArtifactIO      = errors.New("artifact could not be read")
)

// CanonicalWorkspace matches the workspace identity recorded by core's loop.
func CanonicalWorkspace(workspace string) (string, error) {
	if !filepath.IsAbs(workspace) {
		return "", domain.ErrInvalidReference
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", artifactError(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", artifactError(err)
	}
	if !info.IsDir() {
		return "", ErrArtifactInvalid
	}
	return root, nil
}

func artifactError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ErrArtifactMissing
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR), errors.Is(err, os.ErrInvalid):
		return ErrArtifactInvalid
	default:
		return ErrArtifactIO
	}
}

func openArtifact(root *os.Root, path string, limit int64) (*os.File, error) {
	f, err := openSetupFile(root, path)
	if err != nil {
		return nil, artifactError(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, artifactError(err)
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		f.Close()
		return nil, ErrArtifactInvalid
	}
	return f, nil
}

// ExactPlan accepts an explicit core plan location, including done/, and never
// searches for a replacement. Parsing and identity digests belong to core.
func ExactPlan(workspace, path string) (routing.Plan, error) {
	slug := strings.TrimSuffix(filepath.Base(path), ".md")
	if strings.HasPrefix(path, ".batuta/plan-") {
		slug = strings.TrimPrefix(slug, "plan-")
	}
	active := routing.PlanLocations(slug)
	archived := filepath.ToSlash(filepath.Join(".batuta", "plans", "done", slug+".md"))
	if path != active[0] && path != active[1] && path != archived {
		return routing.Plan{}, domain.ErrInvalidReference
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return routing.Plan{}, artifactError(err)
	}
	defer root.Close()
	f, err := openArtifact(root, path, 1<<20)
	if err != nil {
		return routing.Plan{}, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return routing.Plan{}, artifactError(err)
	}
	plan, err := routing.ParsePlan(slug, body)
	if err != nil {
		return routing.Plan{}, ErrArtifactInvalid
	}
	// Active and legacy sources must not offer competing identities. Archived
	// paths remain explicit even when a same-named active replacement exists.
	if path != archived {
		for _, other := range active {
			if other == path {
				continue
			}
			alt, err := openArtifact(root, other, 1<<20)
			if err == nil {
				alt.Close()
				return routing.Plan{}, ErrArtifactInvalid
			}
			if !errors.Is(err, ErrArtifactMissing) {
				return routing.Plan{}, err
			}
		}
	}
	plan.Path = path
	return plan, nil
}

// readJournal uses core's chain verifier on an anchored, bounded descriptor.
// Unlike journal.Open it has no directory-creation side effect.
func readJournal(workspace, delivery string) ([]journal.Record, error) {
	if !journal.ValidDeliveryID(delivery) {
		return nil, domain.ErrInvalidReference
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, artifactError(err)
	}
	defer root.Close()
	const limit = 256 << 20
	f, err := openArtifact(root, journal.Dir+"/"+delivery+".jsonl", limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := &io.LimitedReader{R: f, N: limit + 1}
	records, err := journal.Decode(reader)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return nil, ErrArtifactIO
		}
		return nil, ErrArtifactInvalid
	}
	if reader.N <= 0 {
		return nil, ErrArtifactInvalid
	}
	if len(records) == 0 {
		return nil, ErrArtifactMissing
	}
	return records, nil
}

func deliveryRecords(workspace string, b domain.MissionBinding) ([]journal.Record, error) {
	records, err := readJournal(workspace, b.DeliveryID)
	if err != nil {
		return nil, err
	}
	if records[0].Kind != loop.KindOpened {
		return nil, ErrArtifactInvalid
	}
	for _, record := range records[1:] {
		if record.Kind == loop.KindOpened {
			return nil, ErrArtifactInvalid
		}
	}
	var opened struct {
		Slug       string `json:"slug"`
		PlanPath   string `json:"plan_path"`
		PlanDigest string `json:"plan_digest"`
		Workspace  string `json:"workspace"`
	}
	if json.Unmarshal(records[0].Detail, &opened) != nil {
		return nil, ErrArtifactInvalid
	}
	pathMatches := opened.PlanPath == b.PlanPath
	if b.PlanPath == ".batuta/plans/done/"+b.PlanSlug+".md" {
		for _, path := range routing.PlanLocations(b.PlanSlug) {
			pathMatches = pathMatches || opened.PlanPath == path
		}
	}
	if opened.Slug != b.PlanSlug || opened.PlanDigest != b.PlanDigest || opened.Workspace != workspace || !pathMatches {
		return nil, ErrArtifactInvalid
	}
	if b.OpenedDigest != "" && b.OpenedDigest != records[0].Digest {
		return nil, ErrArtifactInvalid
	}
	var graph routing.DeliveryGraph
	if len(records[len(records)-1].Graph) == 0 || string(records[len(records)-1].Graph) == "null" || json.Unmarshal(records[len(records)-1].Graph, &graph) != nil {
		return nil, ErrArtifactInvalid
	}
	return records, nil
}

func ValidateDelivery(workspace string, b domain.MissionBinding) (string, error) {
	records, err := deliveryRecords(workspace, b)
	if err != nil {
		return "", err
	}
	return records[0].Digest, nil
}

func ProjectMission(workspace string, b domain.MissionBinding) (domain.WatchView, error) {
	out := domain.WatchView{API: API, Workspace: workspace, Delivery: b.DeliveryID, Source: domain.SourceUnavailable}
	plan, err := ExactPlan(workspace, b.PlanPath)
	if err != nil {
		return out, err
	}
	if plan.Slug != b.PlanSlug || plan.Set.Digest != b.PlanDigest {
		return out, ErrArtifactInvalid
	}
	out.Plans = []domain.PlanRef{{Name: plan.Slug, Status: string(plan.Status), Done: strings.Contains(plan.Path, "/done/") || plan.Status == routing.PlanDone}}
	if b.DeliveryID == "" {
		out.Message = "delivery_unassigned"
		return out, nil
	}
	records, err := deliveryRecords(workspace, b)
	if err != nil {
		return out, err
	}
	projected := dto(workspace, b.DeliveryID, loop.PanelModel(records, time.Now().UTC(), ""))
	projected.PendingQuestion = pendingQuestion(records, projected)
	projected.Plans = out.Plans
	projected.Source = domain.SourceCurrent
	return projected, nil
}
