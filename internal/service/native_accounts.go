package service

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/inventory"
)

type nativeAccount struct {
	account    domain.Account
	executable string
	env        []string
	members    []domain.Member
	adapter    executor.Adapter
}

func (a nativeAccount) permits(model, effort string) bool {
	for _, member := range a.members {
		if member.Model == model && (member.Effort == "" || member.Effort == effort) {
			return true
		}
	}
	return false
}

func (a nativeAccount) permitsModel(model string) bool {
	for _, member := range a.members {
		if member.Model == model {
			return true
		}
	}
	return false
}

type nativeAccounts struct {
	manager *session.Manager
	byCLI   map[string]nativeAccount
}

func (s *Service) nativeAccounts(ctx context.Context, mission domain.Mission, root string) (nativeAccounts, string, error) {
	out := nativeAccounts{manager: s.Session, byCLI: map[string]nativeAccount{}}
	if s.Session == nil || len(mission.SquadSnapshot.Members) == 0 {
		return out, "", domain.ErrExecutionUnavailable
	}
	if _, err := domain.SelectSnapshotMember(mission.SquadSnapshot, "", true); err != nil {
		return out, "", err
	}
	maestros := 0
	for _, member := range mission.SquadSnapshot.Members {
		if member.Maestro {
			maestros++
		}
	}
	if maestros != 1 {
		return out, "", fmt.Errorf("%w: native execution requires one explicit maestro", domain.ErrInvalidReference)
	}
	seen := map[string]bool{}
	for _, member := range mission.SquadSnapshot.Members {
		if seen[member.ID] || member.ID == "" || (member.CLI != "codex" && member.CLI != "claude") || member.AccountID == "" || strings.TrimSpace(member.Model) == "" || member.Model == executor.DefaultModel {
			return out, "", domain.ErrInvalidReference
		}
		seen[member.ID] = true
		a, ok := out.byCLI[member.CLI]
		if ok && a.account.ID != member.AccountID {
			return out, "", fmt.Errorf("%w: ambiguous account for %s", domain.ErrInvalidReference, member.CLI)
		}
		if !ok {
			account, err := s.Store.GetAccount(member.AccountID)
			if err != nil {
				return out, "", err
			}
			if account.Adapter != member.CLI {
				return out, "", domain.ErrInvalidReference
			}
			r := s.readinessChecker().Check(ctx, s.Home, account)
			if !r.Ready {
				return out, "", ReadinessError{Code: r.Code, AccountID: account.ID}
			}
			ad, err := adapters.Get(member.CLI)
			if err != nil {
				return out, "", err
			}
			executable, err := ad.Lookup()
			if err != nil {
				return out, "", err
			}
			env, err := ad.IsolateEnv(s.Home, account)
			if err != nil {
				return out, "", err
			}
			a = nativeAccount{account: account, executable: executable, env: env}
		}
		a.members = append(a.members, member)
		out.byCLI[member.CLI] = a
	}
	skills := ""
	for _, cli := range out.sortedCLIs() {
		a := out.byCLI[cli]
		var roles []string
		for _, member := range a.members {
			roles = append(roles, member.Skills...)
		}
		p, err := prepareSessionSkills(root, true, cli, a.env, roles)
		if err != nil {
			return out, "", err
		}
		if !p.Enabled || p.SkillsRoot == "" || (skills != "" && skills != p.SkillsRoot) {
			return out, "", domain.ErrExecutionUnavailable
		}
		skills = p.SkillsRoot
		a.adapter, err = executor.LoadAdapter(skills, cli)
		if err != nil {
			return out, "", err
		}
		a.env = accounts.RuntimeEnvironment(os.Environ(), append(append([]string{}, p.Env...), a.env...))
		out.byCLI[cli] = a
	}
	return out, skills, nil
}

func (a nativeAccounts) sortedCLIs() []string {
	names := make([]string, 0, len(a.byCLI))
	for cli := range a.byCLI {
		names = append(names, cli)
	}
	sort.Strings(names)
	return names
}

func (a nativeAccounts) inventory(ctx context.Context, root string) (inventory.InventorySnapshot, error) {
	selected := make([]batuta.AccountInventory, 0, len(a.byCLI))
	for _, cli := range a.sortedCLIs() {
		account := a.byCLI[cli]
		selected = append(selected, batuta.AccountInventory{CLI: cli, Executable: account.executable, Runner: session.NativeRunner{Manager: a.manager, Environment: account.env}})
	}
	return batuta.CollectAccountInventory(ctx, root, selected)
}
