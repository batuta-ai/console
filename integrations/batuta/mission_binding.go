package batuta

import (
	"context"
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

// CanonicalWorkspace returns the physical workspace identity supplied to Core.
func CanonicalWorkspace(workspace string) (string, error) {
	if !filepath.IsAbs(workspace) {
		return "", domain.ErrInvalidReference
	}
	root, err := canonicalWorkspacePath(workspace)
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
	plan, _, err := ExactPlanContent(workspace, path)
	return plan, err
}

// Match only the native catalog location or its canonical slash spelling;
// the caller's path remains the source identity and is never cleaned.
func matchesPlanLocation(path, native string) bool {
	return path == native || path == filepath.ToSlash(native)
}

// IsArchivedPlanPath recognizes only the explicit archive location for slug.
func IsArchivedPlanPath(path, slug string) bool {
	return matchesPlanLocation(path, filepath.Join(".batuta", "plans", "done", slug+".md"))
}

// ExactPlanContent returns the bytes parsed by Core from the same bounded read.
func ExactPlanContent(workspace, path string) (routing.Plan, []byte, error) {
	slug := strings.TrimSuffix(filepath.Base(path), ".md")
	if strings.HasPrefix(path, ".batuta/plan-") || strings.HasPrefix(path, filepath.Join(".batuta", "plan-")) {
		slug = strings.TrimPrefix(slug, "plan-")
	}
	active := routing.PlanLocations(slug)
	archived := filepath.Join(".batuta", "plans", "done", slug+".md")
	if !matchesPlanLocation(path, active[0]) && !matchesPlanLocation(path, active[1]) && !matchesPlanLocation(path, archived) {
		return routing.Plan{}, nil, domain.ErrInvalidReference
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return routing.Plan{}, nil, artifactError(err)
	}
	defer root.Close()
	f, err := openArtifact(root, path, 1<<20)
	if err != nil {
		return routing.Plan{}, nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return routing.Plan{}, nil, artifactError(err)
	}
	body, err := io.ReadAll(io.LimitReader(f, domain.MaxPlanBytes+1))
	if err != nil {
		return routing.Plan{}, nil, artifactError(err)
	}
	after, err := f.Stat()
	if err != nil {
		return routing.Plan{}, nil, artifactError(err)
	}
	if len(body) > domain.MaxPlanBytes || int64(len(body)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return routing.Plan{}, nil, ErrArtifactInvalid
	}
	plan, err := routing.ParsePlan(slug, body)
	if err != nil {
		return routing.Plan{}, nil, ErrArtifactInvalid
	}
	// Active and legacy sources must not offer competing identities. Archived
	// paths remain explicit even when a same-named active replacement exists.
	if !matchesPlanLocation(path, archived) {
		for _, other := range active {
			if matchesPlanLocation(path, other) {
				continue
			}
			alt, err := openArtifact(root, other, 1<<20)
			if err == nil {
				alt.Close()
				return routing.Plan{}, nil, ErrArtifactInvalid
			}
			if !errors.Is(err, ErrArtifactMissing) {
				return routing.Plan{}, nil, err
			}
		}
	}
	plan.Path = path
	return plan, body, nil
}

// readJournalSnapshot binds Core's capture to an anchored, read-only descriptor.
// Ownership and the descriptor are released before projection or other locks.
func readJournalSnapshot(ctx context.Context, workspace, delivery string) (snapshot journal.Snapshot, resultErr error) {
	// Compatibility observers have no caller context; every capture must still
	// release its descriptor when a writer retains journal ownership.
	ctx, cancel := context.WithTimeoutCause(ctx, 5*time.Second, ErrArtifactIO)
	defer cancel()
	if !journal.ValidDeliveryID(delivery) {
		return journal.Snapshot{}, domain.ErrInvalidReference
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return journal.Snapshot{}, artifactError(err)
	}
	defer root.Close()
	f, err := openArtifact(root, journal.Dir+"/"+delivery+".jsonl", 256<<20)
	if err != nil {
		return journal.Snapshot{}, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			snapshot = journal.Snapshot{}
			resultErr = errors.Join(resultErr, ErrArtifactIO)
		}
	}()
	snapshot, err = journal.ReadSnapshot(ctx, f)
	if err != nil {
		var pathErr *os.PathError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (context.Cause(ctx) != nil && errors.Is(err, context.Cause(ctx))) {
			return journal.Snapshot{}, err
		}
		if errors.As(err, &pathErr) {
			return journal.Snapshot{}, ErrArtifactIO
		}
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	if len(snapshot.Records()) == 0 {
		return journal.Snapshot{}, ErrArtifactMissing
	}
	if !snapshot.Complete() {
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	return snapshot, nil
}

func readJournal(workspace, delivery string) ([]journal.Record, error) {
	snapshot, err := readJournalSnapshot(context.Background(), workspace, delivery)
	if err != nil {
		return nil, err
	}
	return snapshot.Records(), nil
}

func deliverySnapshot(ctx context.Context, workspace string, b domain.MissionBinding) (journal.Snapshot, error) {
	snapshot, err := readJournalSnapshot(ctx, workspace, b.DeliveryID)
	if err != nil {
		return journal.Snapshot{}, err
	}
	records := snapshot.Records()
	if records[0].Kind != loop.KindOpened {
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	for _, record := range records[1:] {
		if record.Kind == loop.KindOpened {
			return journal.Snapshot{}, ErrArtifactInvalid
		}
	}
	var opened struct {
		Slug       string `json:"slug"`
		PlanPath   string `json:"plan_path"`
		PlanDigest string `json:"plan_digest"`
		Workspace  string `json:"workspace"`
	}
	if json.Unmarshal(records[0].Detail, &opened) != nil {
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	pathMatches := opened.PlanPath == b.PlanPath
	if IsArchivedPlanPath(b.PlanPath, b.PlanSlug) {
		for _, path := range routing.PlanLocations(b.PlanSlug) {
			pathMatches = pathMatches || matchesPlanLocation(opened.PlanPath, path)
		}
	}
	if opened.Slug != b.PlanSlug || opened.PlanDigest != b.PlanDigest || opened.Workspace != workspace || !pathMatches {
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	if b.OpenedDigest != "" && b.OpenedDigest != records[0].Digest {
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	var graph routing.DeliveryGraph
	if len(records[len(records)-1].Graph) == 0 || string(records[len(records)-1].Graph) == "null" || json.Unmarshal(records[len(records)-1].Graph, &graph) != nil {
		return journal.Snapshot{}, ErrArtifactInvalid
	}
	return snapshot, nil
}

func deliveryRecords(ctx context.Context, workspace string, b domain.MissionBinding) ([]journal.Record, error) {
	snapshot, err := deliverySnapshot(ctx, workspace, b)
	if err != nil {
		return nil, err
	}
	return snapshot.Records(), nil
}

func ValidateDelivery(workspace string, b domain.MissionBinding) (string, error) {
	return ValidateDeliveryContext(context.Background(), workspace, b)
}

func ValidateDeliveryContext(ctx context.Context, workspace string, b domain.MissionBinding) (string, error) {
	records, err := deliveryRecords(ctx, workspace, b)
	if err != nil {
		return "", err
	}
	return records[0].Digest, nil
}

func ProjectMission(workspace string, b domain.MissionBinding) (domain.WatchView, error) {
	return ProjectMissionContext(context.Background(), workspace, b)
}

func ProjectMissionContext(ctx context.Context, workspace string, b domain.MissionBinding) (domain.WatchView, error) {
	out := domain.WatchView{API: API, Workspace: workspace, Delivery: b.DeliveryID, Source: domain.SourceUnavailable}
	plan, err := ExactPlan(workspace, b.PlanPath)
	if err != nil {
		return out, err
	}
	if plan.Slug != b.PlanSlug || plan.Set.Digest != b.PlanDigest {
		return out, ErrArtifactInvalid
	}
	out.Plans = []domain.PlanRef{{Name: plan.Slug, Status: string(plan.Status), Done: IsArchivedPlanPath(plan.Path, plan.Slug) || plan.Status == routing.PlanDone}}
	if b.DeliveryID == "" {
		out.Message = "delivery_unassigned"
		return out, nil
	}
	records, err := deliveryRecords(ctx, workspace, b)
	if err != nil {
		return out, err
	}
	projected := dto(workspace, b.DeliveryID, loop.PanelModel(records, time.Now().UTC(), ""))
	projected.PendingQuestion = pendingQuestion(records, projected)
	projected.Plans = out.Plans
	projected.Source = domain.SourceCurrent
	return projected, nil
}
