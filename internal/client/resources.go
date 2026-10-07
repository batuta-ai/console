package client

import (
	"context"
	"net/url"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/buildinfo"
	"github.com/batuta-ai/console/internal/domain"
)

type CreateAccountRequest struct {
	Label   string `json:"label"`
	Adapter string `json:"adapter"`
}
type RenameAccountRequest struct {
	Label string `json:"label"`
}
type LoginAccountRequest struct {
	Owner  string `json:"owner"`
	Client string `json:"client"`
}
type CreateWorkspaceRequest struct {
	Name string `json:"name"`
	Root string `json:"root"`
}
type PickRootResult struct {
	Path     string `json:"path,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
}
type InitWorkspaceRequest struct {
	AccountID string             `json:"account_id"`
	Config    domain.SetupConfig `json:"config"`
	Brief     string             `json:"brief"`
	Owner     string             `json:"owner"`
	Client    string             `json:"client"`
}
type StartSessionRequest struct {
	MissionID string   `json:"mission_id"`
	MemberID  string   `json:"member_id,omitempty"`
	AccountID string   `json:"account_id,omitempty"`
	Adapter   string   `json:"adapter"`
	Argv      []string `json:"argv,omitempty"`
	Inject    *bool    `json:"batuta"`
	Owner     string   `json:"owner,omitempty"`
	Client    string   `json:"client,omitempty"`
}
type ControlSessionRequest struct {
	Surface string `json:"surface"`
	Client  string `json:"client"`
}
type CreateMissionRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Brief       string `json:"brief"`
	SquadID     string `json:"squad_id"`
}
type MissionBindingRequest struct {
	WorkspaceID string `json:"workspace_id"`
	PlanPath    string `json:"plan_path"`
	DeliveryID  string `json:"delivery_id,omitempty"`
}
type SquadValidation struct {
	Validation domain.Validation `json:"validation"`
	Squad      domain.Squad      `json:"squad"`
}
type SkillsResult struct {
	Root          string         `json:"root"`
	InjectDefault bool           `json:"inject_default"`
	Skills        []batuta.Skill `json:"skills"`
}
type BuildCapability struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified *bool  `json:"modified"`
}
type RuntimeCapability struct {
	OS        string          `json:"os"`
	Arch      string          `json:"arch"`
	GoVersion string          `json:"go_version"`
	Selected  bool            `json:"selected"`
	Build     BuildCapability `json:"build"`
}
type ReleaseMatrix struct {
	Platforms     []string              `json:"platforms"`
	Providers     []string              `json:"providers"`
	Qualification domain.CapabilityFact `json:"qualification"`
}
type CapabilitiesResult struct {
	Provenance   buildinfo.Info         `json:"provenance"`
	API          string                 `json:"api"`
	Product      string                 `json:"product"`
	Lang         string                 `json:"lang"`
	Integrations []string               `json:"integrations"`
	Adapters     []string               `json:"adapters"`
	Features     map[string]bool        `json:"features"`
	Runtime      RuntimeCapability      `json:"runtime"`
	Matrix       ReleaseMatrix          `json:"matrix"`
	PTY          domain.PTYCapability   `json:"pty"`
	Providers    []domain.CLICapability `json:"providers"`
}

func (c *Client) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	return operationResult[[]domain.Account](c, ctx, "GET", "/v0/accounts", nil)
}
func (c *Client) CreateAccount(ctx context.Context, in CreateAccountRequest) (domain.Account, error) {
	return operationResult[domain.Account](c, ctx, "POST", "/v0/accounts", in)
}
func (c *Client) RenameAccount(ctx context.Context, id string, in RenameAccountRequest) (domain.Account, error) {
	return resourceResult[domain.Account](c, ctx, "PATCH", "accounts", id, "", in)
}
func (c *Client) ConnectAccount(ctx context.Context, id string) (domain.Account, error) {
	return resourceResult[domain.Account](c, ctx, "POST", "accounts", id, "/connect", nil)
}
func (c *Client) LoginAccount(ctx context.Context, id string, in LoginAccountRequest) (domain.Session, error) {
	return resourceResult[domain.Session](c, ctx, "POST", "accounts", id, "/login", in)
}
func (c *Client) ListWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	return operationResult[[]domain.Workspace](c, ctx, "GET", "/v0/workspaces", nil)
}
func (c *Client) CreateWorkspace(ctx context.Context, in CreateWorkspaceRequest) (domain.Workspace, error) {
	return operationResult[domain.Workspace](c, ctx, "POST", "/v0/workspaces", in)
}
func (c *Client) PickWorkspaceRoot(ctx context.Context) (PickRootResult, error) {
	return operationResult[PickRootResult](c, ctx, "POST", "/v0/workspaces/pick-root", nil)
}
func (c *Client) InitWorkspace(ctx context.Context, id string, in InitWorkspaceRequest) (domain.Session, error) {
	return resourceResult[domain.Session](c, ctx, "POST", "workspaces", id, "/init", in)
}
func (c *Client) ListSessions(ctx context.Context) ([]domain.Session, error) {
	return operationResult[[]domain.Session](c, ctx, "GET", "/v0/sessions", nil)
}
func (c *Client) StartSession(ctx context.Context, in StartSessionRequest) (domain.Session, error) {
	return operationResult[domain.Session](c, ctx, "POST", "/v0/sessions", in)
}
func (c *Client) StopSession(ctx context.Context, id string) (domain.Session, error) {
	return resourceResult[domain.Session](c, ctx, "POST", "sessions", id, "/stop", nil)
}
func (c *Client) ControlSession(ctx context.Context, id string, in ControlSessionRequest) (domain.Session, error) {
	return resourceResult[domain.Session](c, ctx, "POST", "sessions", id, "/control", in)
}
func (c *Client) ListSquads(ctx context.Context) ([]domain.Squad, error) {
	return operationResult[[]domain.Squad](c, ctx, "GET", "/v0/squads", nil)
}
func (c *Client) SaveSquad(ctx context.Context, in domain.Squad) (domain.Squad, error) {
	return operationResult[domain.Squad](c, ctx, "POST", "/v0/squads", in)
}
func (c *Client) SaveSquadID(ctx context.Context, id string) (domain.Squad, error) {
	return resourceResult[domain.Squad](c, ctx, "POST", "squads", id, "/save", nil)
}
func (c *Client) ValidateSquad(ctx context.Context, id string, draft *domain.Squad) (SquadValidation, error) {
	return resourceResult[SquadValidation](c, ctx, "POST", "squads", id, "/validate", draft)
}
func (c *Client) ListMissions(ctx context.Context, workspace string) ([]domain.Mission, error) {
	return operationResult[[]domain.Mission](c, ctx, "GET", "/v0/missions?"+url.Values{"workspace": {workspace}}.Encode(), nil)
}
func (c *Client) CreateMission(ctx context.Context, in CreateMissionRequest) (domain.Mission, error) {
	return operationResult[domain.Mission](c, ctx, "POST", "/v0/missions", in)
}
func (c *Client) BindMission(ctx context.Context, id string, in MissionBindingRequest) (domain.Mission, error) {
	return resourceResult[domain.Mission](c, ctx, "PUT", "missions", id, "/binding", in)
}
func (c *Client) MissionWatch(ctx context.Context, id, workspace, delivery string) (domain.WatchView, error) {
	return resourceResult[domain.WatchView](c, ctx, "GET", "missions", id, "/watch?"+url.Values{"workspace": {workspace}, "delivery": {delivery}}.Encode(), nil)
}
func (c *Client) WorkspaceWatch(ctx context.Context, workspace, delivery string) (domain.WatchView, error) {
	return operationResult[domain.WatchView](c, ctx, "GET", "/v0/watch?"+url.Values{"workspace": {workspace}, "delivery": {delivery}}.Encode(), nil)
}
func (c *Client) Skills(ctx context.Context) (SkillsResult, error) {
	return operationResult[SkillsResult](c, ctx, "GET", "/v0/skills", nil)
}
func (c *Client) Capabilities(ctx context.Context) (CapabilitiesResult, error) {
	return operationResult[CapabilitiesResult](c, ctx, "GET", "/v0/capabilities", nil)
}
func (c *Client) Limits(ctx context.Context) ([]domain.LimitSnapshot, error) {
	return operationResult[[]domain.LimitSnapshot](c, ctx, "GET", "/v0/limits", nil)
}
func (c *Client) Activity(ctx context.Context) ([]domain.Event, error) {
	return operationResult[[]domain.Event](c, ctx, "GET", "/v0/activity", nil)
}
