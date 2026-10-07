package tui

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func TestCatalogRetryPlainEnterSafetyAndGuidance(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			calls := 0
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				var in client.CreateMissionRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in != (client.CreateMissionRequest{WorkspaceID: "explicit-workspace", SquadID: "explicit-squad", Title: "Exact objective", Brief: "Exact brief\nqnsvfd"}) {
					t.Errorf("body: %+v", in)
				}
				_ = json.NewEncoder(w).Encode(domain.Mission{ID: "once", WorkspaceID: in.WorkspaceID, SquadID: in.SquadID, Title: in.Title, Brief: in.Brief, Status: "open", SquadSnapshot: domain.Squad{ID: in.SquadID, Version: 9}})
			})
			m := conductorMission(New(c).(model))
			m = catalogTabs(m, 1)
			m, cmd := catalogKey(m, "enter")
			if cmd != nil || m.catalog.mission.reviewing {
				t.Fatal("title Enter submitted")
			}
			m = catalogTabs(m, 1)
			m, cmd = catalogKey(m, "enter")
			m = catalogPaste(m, "qnsvfd")
			if cmd != nil || m.catalog.mission.reviewing || m.catalog.mission.brief != "Exact brief\nqnsvfd" {
				t.Fatal("brief Enter stopped being newline")
			}
			m = catalogTabs(m, 1)
			accountText(t, m, map[string]string{"en": "enter on workspace/squad to review", "pt": "enter em workspace/squad para revisar"}[lang])
			m, cmd = catalogKey(m, "enter")
			if cmd != nil || calls != 0 || !m.catalog.mission.reviewing {
				t.Fatal("review must not dispatch")
			}
			accountText(t, m, map[string]string{"en": "Confirm catalog creation: enter", "pt": "Confirmar criação no catálogo: enter"}[lang])
			accountText(t, m, "explicit-workspace")
			accountText(t, m, "explicit-squad")
			m, _ = catalogKey(m, "esc")
			if m.catalog.mission.reviewing || m.screen != scrMissionForm {
				t.Fatal("escape did not return to edit")
			}
			m, _ = catalogKey(m, "enter")
			m, cmd = catalogKey(m, "enter")
			if cmd == nil {
				t.Fatal("Enter did not confirm")
			}
			m, duplicate := catalogKey(m, "enter")
			if duplicate != nil {
				t.Fatal("duplicate create")
			}
			m = catalogFinish(t, m, cmd)
			if calls != 1 || m.missionID != "once" || m.catalog.selected.SquadSnapshot.Version != 9 || m.catalog.notice != "catalog_snapshot_changed" {
				t.Fatal("canonical receipt/once semantics changed")
			}
		})
	}
}

func TestCatalogRetryMissionObjectiveNormalization(t *testing.T) {
	for _, mode := range []string{"normalized", "padded-title", "padded-brief", "changed-title", "changed-brief"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			want := client.CreateMissionRequest{WorkspaceID: "explicit-workspace", SquadID: "explicit-squad", Title: "  Exact objective  ", Brief: "\n  Exact\nbrief  \n"}
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				var in client.CreateMissionRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in != want {
					t.Errorf("request normalization changed body: %+v", in)
				}
				out := domain.Mission{ID: "reply-id", WorkspaceID: in.WorkspaceID, SquadID: in.SquadID, Title: strings.TrimSpace(in.Title), Brief: strings.TrimSpace(in.Brief), Status: "open", SquadSnapshot: domain.Squad{ID: in.SquadID, Version: 17}}
				switch mode {
				case "padded-title":
					out.Title = in.Title
				case "padded-brief":
					out.Brief = in.Brief
				case "changed-title":
					out.Title = "foreign"
				case "changed-brief":
					out.Brief = "foreign"
				}
				_ = json.NewEncoder(w).Encode(out)
			})
			m := conductorMission(New(c).(model))
			m.catalog.mission.title, m.catalog.mission.brief = want.Title, want.Brief
			before := m.catalog.mission
			m, _ = catalogKey(m, "ctrl+enter")
			m, cmd := catalogKey(m, "ctrl+enter")
			m = catalogFinish(t, m, cmd)
			if calls != 1 {
				t.Fatal("count")
			}
			if mode == "normalized" {
				if m.missionID != "reply-id" || m.catalog.uncertain != nil || m.catalog.selected.Title != "Exact objective" || m.catalog.selected.Brief != "Exact\nbrief" || m.catalog.selected.SquadSnapshot.Version != 17 {
					t.Fatal("service normalization or canonical snapshot rejected")
				}
			} else {
				if m.missionID != "" || m.catalog.selected.ID != "" || m.catalog.uncertain == nil || len(m.catalog.receipts) != 1 || m.catalog.receipts[0].mission.ID != "reply-id" || m.catalog.mission.request() != before.request() {
					t.Fatal("foreign objective adopted or receipt/draft lost")
				}
				m, cmd = catalogKey(m, "ctrl+enter")
				if cmd != nil || calls != 1 {
					t.Fatal("unknown replay")
				}
			}
		})
	}
}

func TestCatalogRetryNoOpPreservesAuthority(t *testing.T) {
	for _, focus := range []int{0, 1, 2, 4, 5, 6, 7} {
		for _, key := range []string{"left", "right", "up", "backspace"} {
			t.Run(string(rune('0'+focus))+"/"+key, func(t *testing.T) {
				m := New(nil).(model).startSquad(domain.Squad{Members: []domain.Member{{CLI: "codex"}}})
				m = catalogTabs(m, focus)
				m.catalog.validation = &domain.Validation{Ready: true, Title: "fresh"}
				before, revision, validation := cloneSquad(m.editor), m.catalog.revision, m.catalog.validation
				m, cmd := catalogKey(m, key)
				if cmd != nil || !reflect.DeepEqual(m.editor, before) || m.catalog.revision != revision || m.catalog.validation != validation {
					t.Fatal("no-op changed settings/revision/validation")
				}
				m = catalogPaste(m, "\x00\r")
				if !reflect.DeepEqual(m.editor, before) || m.catalog.revision != revision || m.catalog.validation != validation {
					t.Fatal("filtered paste invalidated unchanged draft")
				}
			})
		}
	}
	for _, change := range []string{"text", "backspace", "add", "remove", "provider", "account", "member"} {
		t.Run(change, func(t *testing.T) {
			m := New(nil).(model).startSquad(domain.Squad{Name: "edit", Members: []domain.Member{{CLI: "codex"}, {CLI: "claude"}}})
			m.accounts = []domain.Account{{ID: "chosen", Adapter: "codex"}}
			m.catalog.validation = &domain.Validation{Ready: true}
			revision := m.catalog.revision
			switch change {
			case "text":
				m = catalogPaste(m, "x")
			case "backspace":
				m, _ = catalogKey(m, "backspace")
			case "add":
				m, _ = catalogKey(m, "ctrl+n")
			case "remove":
				m, _ = catalogKey(m, "ctrl+x")
			case "member":
				m, _ = catalogKey(m, "ctrl+down")
			case "provider":
				m = catalogTabs(m, 3)
				m, _ = catalogKey(m, "right")
			case "account":
				m = catalogTabs(m, 4)
				m, _ = catalogKey(m, "right")
			}
			if m.catalog.validation != nil || m.catalog.revision <= revision {
				t.Fatal("actual edit kept stale authority")
			}
		})
	}
}

func TestCatalogRetryNoOpKeepsPendingValidationAndInspection(t *testing.T) {
	for _, kind := range []string{"validate", "inspect"} {
		t.Run(kind, func(t *testing.T) {
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					var q domain.Squad
					_ = json.NewDecoder(r.Body).Decode(&q)
					if kind == "inspect" {
						http.Error(w, "unknown", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(client.SquadValidation{Squad: q, Validation: domain.Validation{Ready: true, Title: "current draft"}})
				} else {
					_, _ = w.Write([]byte(`[]`))
				}
			})
			m := catalogSimple(New(c).(model))
			key := "ctrl+v"
			if kind == "inspect" {
				m, cmd := catalogKey(m, "ctrl+s")
				m = catalogFinish(t, m, cmd)
				key = "ctrl+f"
				m, cmd = catalogKey(m, key)
				result := cmd()
				revision := m.catalog.revision
				m, _ = catalogKey(m, "left")
				m = catalogFinish(t, m, func() tea.Msg { return result })
				if m.catalog.revision != revision || !m.catalog.inspected || m.catalog.uncertain == nil {
					t.Fatal("no-op discarded explicit inspection")
				}
				return
			}
			m, cmd := catalogKey(m, key)
			result := cmd()
			m, _ = catalogKey(m, "left")
			m = catalogFinish(t, m, func() tea.Msg { return result })
			if m.catalog.validation == nil || !m.catalog.validation.Ready {
				t.Fatal("no-op discarded exact in-flight validation")
			}
		})
	}
}

func TestCatalogRetryReceiptViewsByOperation(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		for _, kind := range []string{"create", "save"} {
			t.Run(lang+"/"+kind, func(t *testing.T) {
				t.Setenv("BATUTA_LANG", lang)
				writes := 0
				var saved domain.Squad
				c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
					writes++
					if r.URL.Path == "/v0/squads" {
						var q domain.Squad
						_ = json.NewDecoder(r.Body).Decode(&q)
						if writes == 1 {
							q.ID = "prior-editor"
							q.Version = 3
							q.Origin = "custom"
							q.Draft = true
							q.Members[0].ID = "prior-member"
							saved = cloneSquad(q)
						} else {
							q.ID = "unverified-save"
						}
						_ = json.NewEncoder(w).Encode(q)
					} else {
						var in client.CreateMissionRequest
						_ = json.NewDecoder(r.Body).Decode(&in)
						if in != (client.CreateMissionRequest{WorkspaceID: "explicit-workspace", SquadID: "explicit-squad", Title: "Exact objective", Brief: "Exact brief"}) {
							t.Errorf("body %+v", in)
						}
						_ = json.NewEncoder(w).Encode(domain.Mission{ID: "unverified-create", WorkspaceID: "reply-workspace", SquadID: "reply-squad", Title: in.Title, Brief: in.Brief, Status: "open", SquadSnapshot: domain.Squad{ID: "reply-snapshot", Version: 8}})
					}
				})
				m := New(c).(model).startSquad(domain.Squad{Name: "Previous editor", Purpose: "Saved context", Members: []domain.Member{{Role: "Prior lead", CLI: "codex", AccountID: "prior-account", Model: "prior-model", Effort: "high", Skills: []string{"prior-skill"}, Maestro: true}}})
				m, cmd := catalogKey(m, "ctrl+s")
				m = catalogFinish(t, m, cmd)
				if !reflect.DeepEqual(m.editor, saved) {
					t.Fatal("confirmed save missing")
				}
				if kind == "create" {
					m = conductorMission(m)
					m, _ = catalogKey(m, "ctrl+enter")
					m, cmd = catalogKey(m, "ctrl+enter")
				} else {
					m, cmd = catalogKey(m, "ctrl+s")
				}
				m = catalogFinish(t, m, cmd)
				if writes != 2 || m.catalog.uncertain == nil || len(m.catalog.receipts) != 2 || !reflect.DeepEqual(m.editor, saved) {
					t.Fatal("partial recovery/history changed")
				}
				for m.screen != scrMissions && m.screen != scrSquads {
					m, _ = catalogKey(m, "esc")
				}
				view := m.View().Content
				if kind == "create" {
					for _, text := range []string{"Workspace: explicit-workspace", "Squad: explicit-squad", "Exact objective", "Exact brief", "unverified-create", "reply-workspace", "reply-squad", "reply-snapshot", map[string]string{"en": "Retained squad editor (separate from mission selection)", "pt": "Editor de squad mantido (separado da seleção da missão)"}[lang]} {
						accountText(t, m, text)
					}
				} else {
					for _, text := range []string{"prior-editor", "Previous editor", "Saved context", "prior-member", "prior-account", "prior-model", "prior-skill", "unverified-save", map[string]string{"en": "Structural draft (saved catalog fact): true", "pt": "Draft estrutural (fato do catálogo salvo): true"}[lang]} {
						accountText(t, m, text)
					}
					if strings.Contains(view, "Objective title:") || strings.Contains(view, "Título do objetivo:") || strings.Contains(view, "Brief (") {
						t.Fatal("save inspection displays irrelevant mission fields: " + view)
					}
				}
				accountText(t, m, map[string]string{"en": "Unverified reply IDs", "pt": "IDs não confirmados da resposta"}[lang])
			})
		}
	}
}

func TestCatalogRetryMissionNoOpRevision(t *testing.T) {
	for _, focus := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+focus)), func(t *testing.T) {
			m := New(nil).(model).startMission()
			m = catalogTabs(m, focus)
			revision := m.catalog.revision
			m, _ = catalogKey(m, "left")
			m, _ = catalogKey(m, "right")
			m, _ = catalogKey(m, "backspace")
			if m.catalog.revision != revision {
				t.Fatal("no mission field changed but revision invalidated")
			}
		})
	}
}
