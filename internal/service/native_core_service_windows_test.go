package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

// A copied test PE is the local selected CLI fixture. Core's public inventory
// adapter invokes its native diagnostic argv before any test flags exist.
func TestMain(m *testing.M) {
	if handled, code := task43CoreLifetimePE(); handled {
		os.Exit(code)
	}
	if strings.EqualFold(filepath.Base(os.Args[0]), "codex.exe") && len(os.Args) > 1 {
		if handled, code := task43CorePreparationPE(); handled {
			os.Exit(code)
		}
		if handled, code := task43CoreReviewPE(); handled {
			os.Exit(code)
		}
		var output string
		switch strings.Join(os.Args[1:], " ") {
		case "--version":
			output = "codex-cli 0.160.0"
		case "debug models", "debug models --bundled":
			output = `{"models":[{"slug":"qa-native"}]}`
		case "doctor --json --summary":
			output = `{"schema_version":1}`
		case "plugin list --json", "plugin marketplace list --json":
			output = `{}`
		}
		if output != "" {
			if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
				os.Exit(90)
			}
			f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "native-inventory-probes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(91)
			}
			_, err = fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			if err != nil || f.Close() != nil {
				os.Exit(92)
			}
			fmt.Println(output)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func task43CoreSelected(svc *Service, values map[string]nativeBridgeAccount) nativeAccounts {
	selected := nativeAccounts{manager: svc.Session, byCLI: map[string]nativeAccount{}}
	for cli, a := range values {
		selected.byCLI[cli] = nativeAccount{account: domain.Account{ID: "selected", SessionCap: 1}, executable: a.Executable, env: a.Environment, adapter: a.Adapter, members: a.Members}
	}
	return selected
}

func task43CoreCatalogGuard(svc *Service, a domain.PlanApproval, delivery string) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		base, err := svc.planBinding(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID)
		if err != nil {
			return err
		}
		root, err := batuta.CanonicalWorkspace(base.WorkspaceRoot)
		if err != nil {
			return err
		}
		if root != a.Identity.Binding.CanonicalRoot {
			return domain.ErrBindingConflict
		}
		b := a.Identity.Binding
		b.DeliveryID = delivery
		b.OpenedDigest, err = batuta.ValidateApprovedDelivery(root, b, a)
		if err != nil {
			return err
		}
		return svc.Store.AttachApprovedExecution(ctx, a.ID, a.Identity, b)
	}
}

func task43CoreParentRun(t *testing.T, svc *Service, a domain.PlanApproval, skills string, env []string, values map[string]nativeBridgeAccount, resume string) (string, error, bool) {
	t.Helper()
	job, err := svc.native.admit("m", a.Identity.Binding.CanonicalRoot, batuta.ExecutionDelivery(a))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	ctx, cancel := context.WithTimeout(job.ctx, 35*time.Second)
	defer cancel()
	job.ctx = ctx
	release, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var active atomic.Bool
	guard := task43CoreCatalogGuard(svc, a, job.delivery)
	checkedCapacity := false
	linked := func(ctx context.Context) error {
		if !checkedCapacity {
			unexpected, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
			if err == nil {
				unexpected()
				return errors.New("parent capacity was released before Core effects")
			}
			checkedCapacity = true
		}
		return guard(ctx)
	}
	handled, state, runErr := svc.runNativeWindowsExecution(job, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, a, resume, task43CoreSelected(svc, values), skills, env, &active, linked)
	if !handled {
		t.Fatal("actual Windows service protocol branch not selected")
	}
	if runErr == nil && !checkedCapacity {
		t.Fatal("Core never reached actual parent Store guard")
	}
	return state, runErr, active.Load()
}

func TestTask43WindowsNativeCoreParentKeepsReceiptOnResume(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
	if err != nil || state != loop.StateWaitingInput || !active {
		t.Fatal("actual parent start", state, active, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	first, err := svc.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || first.Watch == nil || first.Watch.PendingQuestion == nil || !first.Bound {
		t.Fatal("actual first approved delivery missing", first, err)
	}
	question := *first.Watch.PendingQuestion
	if err := os.Remove(filepath.Join(a.Identity.Binding.CanonicalRoot, a.Identity.Binding.PlanPath)); err != nil {
		t.Fatal(err)
	}
	answer, err := svc.AnswerQuestion(ctx, question.Target.Answer("approved answer"))
	if err != nil || answer.Effect != domain.AnswerRecorded {
		t.Fatal("actual answer after source removal", answer, err)
	}
	state, err, active = task43CoreParentRun(t, svc, a, skills, env, values, first.Delivery)
	if err != nil || state != loop.StateWaitingInput || !active {
		t.Fatal("actual parent resume", state, active, err)
	}
	next, err := svc.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || next.Delivery != first.Delivery || next.OpenedDigest != first.OpenedDigest || next.Watch == nil || next.Watch.PendingQuestion == nil || next.Watch.PendingQuestion.Target.Execution <= question.Target.Execution {
		t.Fatal("resume lost original receipt/opening", next, err)
	}
	launches, err := os.ReadFile(filepath.Join(accounts.ExplicitHome(values["codex"].Environment), "native-launches"))
	if err != nil || string(launches) != "submission\nsubmission\n" {
		t.Fatal("resume duplicated or escaped selected submission", string(launches), err)
	}
	if _, err := os.Stat(filepath.Join(a.Identity.Binding.CanonicalRoot, a.Identity.Binding.PlanPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("helper restored removed mutable source", err)
	}
	lease, err := svc.Session.ReserveNative(map[string]int{"selected": 1})
	if err != nil {
		t.Fatal("parent capacity not released after helper/Release/EOF", err)
	}
	lease()
}

func TestTask43WindowsNativeCoreParentRejectsFreshPlanEdit(t *testing.T) {
	svc, a, skills, env, values := task43CoreApprovedFixture(t)
	svc.NativeExecutorExecutable = task43CoreImage(t)
	path := filepath.Join(a.Identity.Binding.CanonicalRoot, a.Identity.Binding.PlanPath)
	changed := append(append([]byte{}, a.Content...), []byte("\n## Decisions\nChanged after approval.\n")...)
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
	if !errors.Is(err, domain.ErrPlanStale) || state != "" || active {
		t.Fatal("parent lost actual fresh approval rejection", state, active, err)
	}
	if _, err := os.Stat(filepath.Join(accounts.ExplicitHome(values["codex"].Environment), "native-launches")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale receipt launched selected worker", err)
	}
	b, err := svc.planBinding(context.Background(), "m", "w")
	if err != nil || b.DeliveryID != "" || b.OpenedDigest != "" {
		t.Fatal("stale receipt attached delivery", b, err)
	}
}
