package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

var ErrInitConflict = errors.New("workspace init conflicts with the active operation")

type InitInput struct {
	AccountID string             `json:"account_id"`
	Config    domain.SetupConfig `json:"config"`
}

func confirmedInitAnswers(config domain.SetupConfig, brief string) (string, error) {
	fields := []struct{ name, value string }{
		{"Stack", config.Stack}, {"Methodology", config.Methodology}, {"Test", config.Test}, {"Build", config.Build},
		{"Install", config.Install}, {"Execution", config.Execution}, {"Worktree", config.Worktree}, {"Template", config.Template},
	}
	var lines []string
	for _, field := range fields {
		value := strings.TrimSpace(field.value)
		if value == "" || len(value) > 8192 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return "", invalidReference("invalid confirmed %s", field.name)
		}
		lines = append(lines, field.name+": "+value)
	}
	answers := strings.Join(lines, "\n")
	if !batuta.CompleteProfile(loop.ParseProfile(answers)) {
		return "", invalidReference("invalid confirmed profile configuration")
	}
	if len(brief) > 65536 || !utf8.ValidString(brief) || strings.ContainsRune(brief, 0) {
		return "", invalidReference("invalid setup notes")
	}
	if strings.TrimSpace(brief) != "" {
		answers += "\n\nAdditional notes:\n" + strings.TrimSpace(brief)
	}
	return answers, nil
}

func sameInit(current domain.Session, selected string, argv []string) bool {
	return current.AccountID == selected && slices.Equal(current.Argv, argv)
}

func (s *Service) DecorateWorkspaceChecked(w domain.Workspace) (domain.Workspace, error) {
	disk := batuta.ReadSetup(w.Root)
	setup := domain.WorkspaceSetup{Ready: disk.Ready, State: "partial", Code: disk.Code, ConfigurationCode: disk.Code}
	if disk.Code == "profile_missing" {
		setup.State = "missing"
	}
	if disk.Code == "invalid_workspace" {
		setup.State = "invalid_workspace"
	}
	rows, err := s.Store.ListSessions()
	if err != nil {
		return w, catalogReferenceError{err}
	}
	var latest *domain.Session
	for i := range rows {
		row := &rows[i]
		if row.Work != "init" || row.WorkspaceID != w.ID {
			continue
		}
		if latest == nil || row.CreatedAt.After(latest.CreatedAt) || (row.CreatedAt.Equal(latest.CreatedAt) && row.ID > latest.ID) {
			latest = row
		}
	}
	if latest != nil {
		// Exit persistence precedes ownership removal. Observe ownership first,
		// then reload the exact row so a completed exit cannot look interrupted.
		var live domain.Session
		owned := false
		if s.Session != nil {
			live, owned = s.Session.Snapshot(latest.ID)
		}
		row, err := s.Store.GetSession(latest.ID)
		if err != nil {
			return w, catalogReferenceError{err}
		}
		if owned && row.Process != "exited" {
			row = live
		}
		if !owned && row.Process == "running" {
			row.Process = "unknown"
			if row.RecoveryReason == "" {
				row.RecoveryReason = "identity_unverified"
			}
		}
		stopped, err := s.Store.HasSessionStopRequest(row.ID)
		if err != nil {
			return w, catalogReferenceError{err}
		}
		attempt := domain.InitAttempt{SessionID: row.ID, AccountID: row.AccountID, Process: row.Process, StopRequested: stopped, RecoveryReason: row.RecoveryReason, CreatedAt: row.CreatedAt, EndedAt: row.EndedAt}
		switch {
		case owned && row.Process == "running":
			attempt.State = "running"
			if setup.State != "invalid_workspace" {
				setup.State = "running"
				setup.Code = ""
			}
		case row.Process == "unknown" || row.Process == "running":
			attempt.State = "interrupted"
			if setup.State != "invalid_workspace" {
				setup.State = "interrupted"
				setup.Code = "init_interrupted"
			}
		case stopped:
			attempt.State = "cancel_requested"
			if setup.State != "invalid_workspace" {
				setup.State = "cancelled"
				setup.Code = "init_cancel_requested"
			}
		default:
			attempt.State = "exited"
			if !disk.Ready {
				attempt.State = "exited_incomplete"
				if setup.State != "invalid_workspace" {
					setup.State = "partial"
					setup.Code = "init_exited_incomplete"
				}
			}
		}
		setup.Attempt = &attempt
	}
	if disk.Ready && setup.State != "running" {
		setup.State = "ready"
		setup.Code = ""
	}
	w.Profile = disk.Ready
	w.Setup = setup
	return w, nil
}

func catalogValidation(err error) domain.Validation {
	return domain.Validation{Tone: "danger", Title: "Validation: catalog_failure", Code: "catalog_failure", Text: fmt.Sprintf("Catalog lookup failed: %v", err)}
}
