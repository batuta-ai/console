package domain

import (
	"encoding/json"
	"testing"
)

func TestValidateSquadEmpty(t *testing.T) {
	v := ValidateSquad(Squad{})
	if v.Ready || v.Tone != "danger" {
		t.Fatalf("empty squad: %+v", v)
	}
}

func TestValidateSquadReady(t *testing.T) {
	v := ValidateSquad(Squad{Members: []Member{
		{ID: "m1", Role: "Maestro", CLI: "codex", AccountID: "a1", Maestro: true},
	}})
	if !v.Ready {
		t.Fatalf("expected ready, got %+v", v)
	}
}

func TestResolveBindingPrecedence(t *testing.T) {
	got := ResolveBinding(
		Member{Model: "ws-model"},
		Member{Effort: "high"},
		Member{AccountID: "member-acct", CLI: "codex"},
		Member{CLI: "claude", Model: "squad-model", Effort: "low", AccountID: "squad-acct"},
	)
	if got.CLI != "codex" || got.Model != "ws-model" || got.Effort != "high" || got.AccountID != "member-acct" {
		t.Fatalf("%+v", got)
	}
}

func TestValidateSquadNoMaestro(t *testing.T) {
	v := ValidateSquad(Squad{Members: []Member{
		{Role: "Builder", CLI: "codex", AccountID: "a1"},
	}})
	if v.Ready || v.Tone != "warning" {
		t.Fatalf("no maestro: %+v", v)
	}
}

func TestFirstGatesAndAttention(t *testing.T) {
	v := WatchView{
		Available: true,
		Attention: map[string]any{"kind": "question", "text": "Need a decision"},
		Waves: []map[string]any{{
			"rows": []map[string]any{{
				"gates": []string{"pass", "pass", "fail", "pending"},
			}},
		}},
	}
	g := v.FirstGates()
	if g != [4]string{"✓", "✓", "✗", "—"} {
		t.Fatalf("gates %v", g)
	}
	if v.AttentionLine() != "question · Need a decision" {
		t.Fatalf("attention %q", v.AttentionLine())
	}
	none := WatchView{Attention: map[string]any{"kind": "none"}}
	if none.AttentionLine() != "" {
		t.Fatalf("none must be empty, got %q", none.AttentionLine())
	}
}

func TestProcessLifeUnknown(t *testing.T) {
	if ProcessLife("unknown") != "catalog only — process not live" {
		t.Fatal(ProcessLife("unknown"))
	}
}

func TestFormatLimitUnknown(t *testing.T) {
	s := FormatLimit(LimitSnapshot{Source: SourceUnavailable, Origin: "not_qualified"})
	if s[:7] != "unknown" {
		t.Fatalf("%q", s)
	}
}

func TestFirstGatesJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"waves":[{"rows":[{"gates":["pass","fail","pending","silent"]}]}]}`)
	var v WatchView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	g := v.FirstGates()
	if g != [4]string{"✓", "✗", "—", "—"} {
		t.Fatalf("%v", g)
	}
}

func TestCriteriaAndEvidence(t *testing.T) {
	v := WatchView{
		Progress: map[string]any{"criterion": 2.0, "criterion_total": 3.0, "criterion_title": "Tick the phase"},
		Detail:   map[string]any{"criterion_state": "DONE", "last": "task_progress 2 DONE", "log_path": ".batuta/runs/a.log"},
	}
	c := v.Criteria()
	if len(c) != 3 || c[0].State != "pass" || c[1].State != "pass" || c[2].State != "pending" {
		t.Fatalf("%+v", c)
	}
	if c[1].Title != "Tick the phase" {
		t.Fatalf("title %q", c[1].Title)
	}
	ev := v.Evidence()
	if len(ev) < 2 {
		t.Fatalf("%+v", ev)
	}
}

func TestCriteriaFromDetail(t *testing.T) {
	v := WatchView{
		Detail: map[string]any{"criterion": 1.0, "criterion_state": "DONE", "criterion_title": "From detail"},
	}
	c := v.Criteria()
	if len(c) != 1 || c[0].State != "pass" || c[0].Title != "From detail" {
		t.Fatalf("%+v", c)
	}
}

func TestTaskRows(t *testing.T) {
	v := WatchView{Waves: []map[string]any{{
		"rows": []map[string]any{{"task": "task_1", "title": "Checkout", "state": "running", "gates": []string{"pass", "pending", "pending", "pending"}}},
	}}}
	rows := v.TaskRows()
	if len(rows) != 1 || rows[0].Task != "task_1" || rows[0].Gates[0] != "✓" {
		t.Fatalf("%+v", rows)
	}
}

func TestSnapshotSelection(t *testing.T) {
	for _, tc := range []struct {
		name, id, want string
		members        []Member
		inject         bool
		invalid        bool
	}{
		{name: "last maestro", want: "b", members: []Member{{ID: "a", Maestro: true}, {ID: "b", Maestro: true}}},
		{name: "first fallback", want: "a", members: []Member{{ID: "a"}, {ID: "b"}}},
		{name: "explicit", id: "a", want: "a", members: []Member{{ID: "a"}, {ID: "b", Maestro: true}}},
		{name: "empty generic"},
		{name: "empty injected", inject: true, invalid: true},
		{name: "duplicate even after match", id: "a", members: []Member{{ID: "a"}, {ID: "a"}}, invalid: true},
		{name: "empty ID", members: []Member{{ID: ""}}, invalid: true},
		{name: "foreign", id: "foreign", members: []Member{{ID: "a"}}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectSnapshotMember(Squad{Members: tc.members}, tc.id, tc.inject)
			if (err != nil) != tc.invalid || got.ID != tc.want {
				t.Fatalf("got %+v err %v", got, err)
			}
		})
	}
}
