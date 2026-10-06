//go:build darwin || linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeAccountSelectionRejectsImplicitAmbiguousOrUnboundRoutesBeforeSubmission(t *testing.T) {
	for _, scenario := range []string{"implicit_model", "unbound_model", "unbound_effort", "duplicate_member", "no_maestro", "multiple_maestros", "ambiguous_account", "unsupported_cli"} {
		t.Run(scenario, func(t *testing.T) {
			svc, approval, _ := nativeRunFixture(t)
			mission, err := svc.Store.GetMission("m")
			if err != nil {
				t.Fatal(err)
			}
			member := &mission.SquadSnapshot.Members[0]
			switch scenario {
			case "no_maestro":
				member.Maestro = false
			case "implicit_model":
				member.Model = "default"
			case "unbound_model":
				member.Model = "foreign-model"
			case "unbound_effort":
				member.Effort = "high"
			case "duplicate_member", "multiple_maestros", "ambiguous_account":
				other := *member
				if scenario != "duplicate_member" {
					other.ID = "other"
				}
				if scenario == "ambiguous_account" {
					other.Maestro, other.AccountID = false, "unselected-account"
				}
				mission.SquadSnapshot.Members = append(mission.SquadSnapshot.Members, other)
			case "unsupported_cli":
				member.CLI = "opencode"
			}
			if err := svc.Store.PutMission(mission); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
			if err == nil || result.Effect != domain.ExecutionRejected || result.Delivery != "" {
				t.Fatal("unbound selection was admitted", scenario, result, err)
			}
			account, _ := adapters.Get("codex")
			if _, err := os.Stat(filepath.Join(account.IsolateDir(svc.Home, "acct_native"), "native-launches")); !os.IsNotExist(err) {
				t.Fatal("unbound selection submitted a model", scenario, err)
			}
			delivery := domain.ExecutionDelivery(approval.ID, approval.Identity)
			if _, err := os.Stat(filepath.Join(approval.Identity.Binding.CanonicalRoot, ".batuta/journal", delivery+".jsonl")); !os.IsNotExist(err) {
				t.Fatal("unbound selection opened a native delivery", scenario, err)
			}
		})
	}
}

func TestNativeExplicitPrimaryEffortIsNotLostInFallbackPreview(t *testing.T) {
	svc, approval, _ := nativeRunFixture(t)
	path := filepath.Join(os.Getenv("BATUTA_SKILLS"), "adapters/codex.md")
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(bytes), "--model {model}", "--model {model} --effort {effort}")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	mission, err := svc.Store.GetMission("m")
	if err != nil {
		t.Fatal(err)
	}
	mission.SquadSnapshot.Members[0].Effort = "low"
	if err := svc.Store.PutMission(mission); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
	if err != nil || result.Effect != domain.ExecutionStarted {
		t.Fatal("public preview discarded an authorized explicit primary effort", result, err)
	}
	waitNativeQuestionAndIdle(t, ctx, svc, approval, result.Delivery, 0)
}
