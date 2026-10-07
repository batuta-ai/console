package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestNamedResourceWireContracts(t *testing.T)     { testResourceWireContracts(t, false) }
func TestPureNamedResourceWireContracts(t *testing.T) { testResourceWireContracts(t, true) }
func testResourceWireContracts(t *testing.T, pure bool) {
	ctx := context.Background()
	no := false
	account := CreateAccountRequest{"second", "codex"}
	rename := RenameAccountRequest{"renamed"}
	login := LoginAccountRequest{"tui", "client-2"}
	workspace := CreateWorkspaceRequest{"workspace", "/synthetic/root"}
	init := InitWorkspaceRequest{AccountID: "account-2", Config: domain.SetupConfig{Stack: "go", Methodology: "test", Test: "go test", Build: "go build", Install: "none", Execution: "local", Worktree: "yes", Template: "go"}, Brief: "exact brief", Owner: "tui", Client: "client-2"}
	start := StartSessionRequest{MissionID: "mission-2", MemberID: "member-2", AccountID: "account-2", Adapter: "codex", Argv: []string{"synthetic"}, Inject: &no, Owner: "tui", Client: "client-2"}
	control := ControlSessionRequest{"none", "client-2"}
	draft := domain.Squad{ID: "draft-2", Name: "local draft"}
	mission := CreateMissionRequest{"workspace-2", "title", "brief", "squad-2"}
	binding := MissionBindingRequest{"workspace-2", "plans/exact.md", "delivery-2"}
	cases := []struct {
		name, method, path, response string
		body                         any
		call                         func(*Client) (any, error)
	}{
		{"accounts", "GET", "/v0/accounts", `[{"id":"account-2","isolation":"not_yet_qualified"}]`, nil, func(c *Client) (any, error) { return c.ListAccounts(ctx) }},
		{"create account", "POST", "/v0/accounts", `{"id":"account-2"}`, account, func(c *Client) (any, error) { return c.CreateAccount(ctx, account) }},
		{"rename", "PATCH", "/v0/accounts/account%20%3F2", `{"id":"account ?2","label":"renamed"}`, rename, func(c *Client) (any, error) { return c.RenameAccount(ctx, "account ?2", rename) }},
		{"connect", "POST", "/v0/accounts/account-2/connect", `{"id":"account-2"}`, nil, func(c *Client) (any, error) { return c.ConnectAccount(ctx, "account-2") }},
		{"login", "POST", "/v0/accounts/account-2/login", `{"id":"session-2","account_id":"account-2","owner":"tui","owner_client":"client-2"}`, login, func(c *Client) (any, error) { return c.LoginAccount(ctx, "account-2", login) }},
		{"workspaces", "GET", "/v0/workspaces", `[{"id":"workspace-2","root":"/synthetic/root"}]`, nil, func(c *Client) (any, error) { return c.ListWorkspaces(ctx) }},
		{"create workspace", "POST", "/v0/workspaces", `{"id":"workspace-2","root":"/synthetic/root"}`, workspace, func(c *Client) (any, error) { return c.CreateWorkspace(ctx, workspace) }},
		{"picker canceled", "POST", "/v0/workspaces/pick-root", `{"canceled":true}`, nil, func(c *Client) (any, error) { return c.PickWorkspaceRoot(ctx) }},
		{"init", "POST", "/v0/workspaces/workspace-2/init", `{"id":"session-2","account_id":"account-2"}`, init, func(c *Client) (any, error) { return c.InitWorkspace(ctx, "workspace-2", init) }},
		{"sessions recovery", "GET", "/v0/sessions", `[{"id":"session-2","recovery_reason":"process_missing"}]`, nil, func(c *Client) (any, error) { return c.ListSessions(ctx) }},
		{"start", "POST", "/v0/sessions", `{"id":"session-2","mission_id":"mission-2","member_id":"member-2","owner_client":"client-2"}`, start, func(c *Client) (any, error) { return c.StartSession(ctx, start) }},
		{"stop", "POST", "/v0/sessions/session-2/stop", `{"id":"session-2"}`, nil, func(c *Client) (any, error) { return c.StopSession(ctx, "session-2") }},
		{"release", "POST", "/v0/sessions/session-2/control", `{"id":"session-2","owner":"none","owner_client":""}`, control, func(c *Client) (any, error) { return c.ControlSession(ctx, "session-2", control) }},
		{"squads", "GET", "/v0/squads", `[{"id":"squad-2","name":"selected"}]`, nil, func(c *Client) (any, error) { return c.ListSquads(ctx) }},
		{"draft save", "POST", "/v0/squads", `{"id":"draft-2","name":"local draft"}`, draft, func(c *Client) (any, error) { return c.SaveSquad(ctx, draft) }},
		{"save ID", "POST", "/v0/squads/squad-2/save", `{"id":"squad-2"}`, nil, func(c *Client) (any, error) { return c.SaveSquadID(ctx, "squad-2") }},
		{"validate draft", "POST", "/v0/squads/draft-2/validate", `{"validation":{},"squad":{"id":"draft-2","name":"local draft"}}`, draft, func(c *Client) (any, error) { return c.ValidateSquad(ctx, "draft-2", &draft) }},
		{"validate saved", "POST", "/v0/squads/squad-2/validate", `{"validation":{},"squad":{"id":"squad-2"}}`, nil, func(c *Client) (any, error) { return c.ValidateSquad(ctx, "squad-2", nil) }},
		{"missions", "GET", "/v0/missions?workspace=workspace+%26+2", `[{"id":"mission-2","workspace_id":"workspace-2"}]`, nil, func(c *Client) (any, error) { return c.ListMissions(ctx, "workspace & 2") }},
		{"create mission", "POST", "/v0/missions", `{"id":"mission-2","workspace_id":"workspace-2"}`, mission, func(c *Client) (any, error) { return c.CreateMission(ctx, mission) }},
		{"bind", "PUT", "/v0/missions/mission-2/binding", `{"id":"mission-2","workspace_id":"workspace-2","binding":{"workspace_id":"workspace-2","plan_path":"plans/exact.md","delivery_id":"delivery-2","opened_digest":"exact-digest"}}`, binding, func(c *Client) (any, error) { return c.BindMission(ctx, "mission-2", binding) }},
		{"mission watch", "GET", "/v0/missions/mission-2/watch?delivery=delivery+%26+2&workspace=workspace+%26+2", `{"mission_id":"mission-2","workspace_id":"workspace-2","scope":"mission"}`, nil, func(c *Client) (any, error) { return c.MissionWatch(ctx, "mission-2", "workspace & 2", "delivery & 2") }},
		{"workspace watch", "GET", "/v0/watch?delivery=delivery+%26+2&workspace=%2Froot+%26+2", `{"workspace":"/root & 2","scope":"workspace"}`, nil, func(c *Client) (any, error) { return c.WorkspaceWatch(ctx, "/root & 2", "delivery & 2") }},
		{"skills", "GET", "/v0/skills", `{"root":"/synthetic/skills","inject_default":true,"skills":[]}`, nil, func(c *Client) (any, error) { return c.Skills(ctx) }},
		{"capabilities", "GET", "/v0/capabilities", `{"api":"v0","product":"console","lang":"go","integrations":["batuta"],"adapters":["codex"],"features":{"native":true},"runtime":{"os":"linux","arch":"amd64","go_version":"go1.27.1","selected":true,"build":{"version":"unknown","revision":"unknown","modified":null}},"matrix":{"platforms":["linux/amd64"],"providers":["codex"],"qualification":{"status":"pending","code":"not_qualified"}},"pty":{"qualification":{"status":"unknown","code":"unverified"}},"providers":[{"id":"codex","qualification":{"status":"pending","code":"unverified"},"account_readiness":{"status":"unknown","code":"account_required"}}],"provenance":{}}`, nil, func(c *Client) (any, error) { return c.Capabilities(ctx) }},
		{"limits", "GET", "/v0/limits", `[]`, nil, func(c *Client) (any, error) { return c.Limits(ctx) }},
		{"activity", "GET", "/v0/activity", `[{"seq":42,"kind":"selected","payload":{"id":"mission-2"},"at":"2026-10-06T00:00:00Z"}]`, nil, func(c *Client) (any, error) { return c.Activity(ctx) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tt.method || r.URL.RequestURI() != tt.path || r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("request %s %s", r.Method, r.URL.RequestURI())
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if tt.body != nil {
					want, _ := json.Marshal(tt.body)
					var a, b any
					if json.Unmarshal(data, &a) != nil || json.Unmarshal(want, &b) != nil || !reflect.DeepEqual(a, b) {
						t.Errorf("body %s want %s", data, want)
					}
				} else if len(data) != 0 && string(data) != "null" {
					t.Errorf("unexpected body %s", data)
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, tt.response); err != nil {
					t.Error(err)
				}
			})
			c := New("http://fixture", "secret")
			if pure {
				c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, r)
					return rec.Result(), nil
				})
			} else {
				s := httptest.NewServer(handler)
				defer s.Close()
				c = New(s.URL, "secret")
			}
			got, err := tt.call(c)
			if err != nil {
				t.Fatal(err)
			}
			// Decode into the result's actual type, so every represented wire field must survive.
			if caps, ok := got.(CapabilitiesResult); ok {
				if caps.Matrix.Qualification.Status != "pending" || caps.PTY.Qualification.Status != "unknown" || len(caps.Providers) != 1 || caps.Providers[0].AccountReadiness.Status != "unknown" || caps.Runtime.Build.Modified != nil {
					t.Fatalf("qualification facts lost: %+v", caps)
				}
			}
			expected := reflect.New(reflect.TypeOf(got))
			if err := json.Unmarshal([]byte(tt.response), expected.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected.Elem().Interface()) || calls != 1 {
				t.Fatalf("result %#v want %#v calls %d", got, expected.Elem().Interface(), calls)
			}
		})
	}
}
