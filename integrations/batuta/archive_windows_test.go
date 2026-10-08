//go:build windows

package batuta

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func TestTask43WindowsArchivedPlanIdentity(t *testing.T) {
	native := filepath.Join(".batuta", "plans", "done", "alpha.md")
	for _, path := range []string{native, filepath.ToSlash(native)} {
		t.Run(path, func(t *testing.T) {
			root := windowsPlanFixture(t, path)
			plan, body, err := ExactPlanContent(root, path)
			if err != nil || plan.Path != path || len(body) == 0 {
				t.Fatalf("archive identity lost: path=%q plan=%+v error=%v", path, plan, err)
			}
			b := domain.MissionBinding{PlanPath: path, PlanSlug: plan.Slug, PlanDigest: plan.Set.Digest}
			view, err := ProjectMission(root, b)
			if err != nil || len(view.Plans) != 1 || !view.Plans[0].Done || view.Plans[0].Status != string(routing.PlanApproved) {
				t.Fatalf("archive not projected as done: %+v error=%v", view, err)
			}
			if err := os.WriteFile(filepath.Join(root, routing.PlanLocations("alpha")[0]), []byte("foreign active replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			selected, got, err := ExactPlanContent(root, path)
			if err != nil || selected.Path != path || selected.Set.Digest != plan.Set.Digest || !bytes.Equal(got, body) {
				t.Fatalf("archive rebound to active source: %+v error=%v", selected, err)
			}
		})
	}
}

func TestTask43WindowsArchivedDeliveryOpening(t *testing.T) {
	nativeArchive := filepath.Join(".batuta", "plans", "done", "alpha.md")
	nativeActive := routing.PlanLocations("alpha")[0]
	for _, archive := range []string{nativeArchive, filepath.ToSlash(nativeArchive)} {
		for _, active := range []string{nativeActive, filepath.ToSlash(nativeActive)} {
			t.Run(archive+"_"+active, func(t *testing.T) {
				root := windowsPlanFixture(t, archive)
				canonical, err := CanonicalWorkspace(root)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := ExactPlan(canonical, archive)
				if err != nil {
					t.Fatal(err)
				}
				js, err := journal.Open(canonical)
				if err != nil {
					t.Fatal(err)
				}
				detail, err := json.Marshal(map[string]string{"workspace": canonical, "slug": plan.Slug, "plan_path": active, "plan_digest": plan.Set.Digest})
				if err != nil {
					t.Fatal(err)
				}
				opened, err := js.Append("run", journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(js.Path("run"))
				if err != nil {
					t.Fatal(err)
				}
				b := domain.MissionBinding{PlanPath: archive, PlanSlug: plan.Slug, PlanDigest: plan.Set.Digest, DeliveryID: "run", OpenedDigest: opened.Digest}
				digest, err := ValidateDelivery(canonical, b)
				if err != nil || digest != opened.Digest {
					t.Fatalf("native archive lost exact opening: digest=%q error=%v", digest, err)
				}
				for _, change := range []string{"digest", "opening", "path", "slug"} {
					bad := b
					switch change {
					case "digest":
						bad.PlanDigest = "foreign"
					case "opening":
						bad.OpenedDigest = "foreign"
					case "path":
						bad.PlanPath = filepath.Join(".batuta", "plans", "done-other", "alpha.md")
					case "slug":
						bad.PlanSlug = "other"
					}
					if _, err := ValidateDelivery(canonical, bad); !errors.Is(err, ErrArtifactInvalid) {
						t.Fatalf("foreign %s accepted: %v", change, err)
					}
				}
				after, err := os.ReadFile(js.Path("run"))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("opening observation changed journal: %v", err)
				}
			})
		}
	}
}
