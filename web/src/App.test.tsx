import { act, fireEvent, render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider } from "@lyra-ds/react/theme-provider";
import { expect, test, vi } from "vitest";
import { App } from "./App";
import type { Account, Squad, Workspace } from "./lib";
import { setLang } from "./i18n";

const initialWebClient = sessionStorage.getItem("bt-client");

test("creates a workspace from a chosen folder and selects the returned workspace", async () => {
  const workspaces: Workspace[] = [];
  const requests: { path: string; init?: RequestInit }[] = [];
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    requests.push({ path, init });
    if (path === "/v0/workspaces/pick-root") return Response.json({ path: "/tmp/synthetic-checkout" });
    if (path === "/v0/workspaces" && init?.method === "POST") {
      const workspace = { id: "ws-created", ...JSON.parse(String(init.body)), profile: true };
      workspaces.push(workspace);
      return Response.json(workspace);
    }
    if (path === "/v0/workspaces") return Response.json(workspaces);
    if (path.startsWith("/v0/watch")) return Response.json({ available: false });
    if (path === "/v0/skills") return Response.json({ skills: [] });
    return Response.json([]);
  });
  localStorage.setItem("bt-token", "synthetic-token");
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "New workspace" }));
  const dialog = screen.getByRole("dialog", { name: "New workspace" });
  const create = within(dialog).getByRole("button", { name: "Create" });
  expect(create).toBeDisabled();
  await user.click(within(dialog).getByRole("button", { name: "Choose folder" }));
  expect(await within(dialog).findByRole("textbox", { name: "Name" })).toHaveValue("synthetic-checkout");
  await user.clear(within(dialog).getByRole("textbox", { name: "Name" }));
  await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "Journey workspace");
  expect(create).toBeEnabled();
  await user.click(create);
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(await screen.findByRole("button", { name: /Journey workspace/ })).toBeVisible();
  expect(localStorage.getItem("bt-ws")).toBe("ws-created");
  const request = requests.find(({ path, init }) => path === "/v0/workspaces" && init?.method === "POST");
  expect(JSON.parse(String(request?.init?.body))).toEqual({ name: "Journey workspace", root: "/tmp/synthetic-checkout" });
  expect(request?.init?.headers).toEqual({ Authorization: "Bearer synthetic-token", "Content-Type": "application/json" });
});

const confirmedConfig = {
  stack: "React", methodology: "TDD", test: "npm test", build: "npm run build",
  install: "npm ci", execution: "sequential", worktree: "medium+", template: "templates/react.md",
};

function readySetupAccount(adapter: "codex" | "claude" = "codex") {
  const catalog = {
    codex: { provider: "OpenAI", isolation: "supported", session_cap: 3, auth_method: "official_browser" },
    claude: { provider: "Anthropic", isolation: "not_yet_qualified", session_cap: 1, auth_method: "cli_login" },
  };
  return { id: `${adapter}-a`, label: adapter === "codex" ? "Chosen Codex" : "Chosen Claude", adapter, ...catalog[adapter], connected: true, authed: true,
    readiness: { supported: true, available: true, cli_compatible: true, connected: true, credential_configured: true, authenticated: true, ready: true, code: "ready", credential_source: "private_file", checked_at: "2026-10-04T12:00:00Z" } };
}

function setupAPI(existing = true, adapter: "codex" | "claude" = "codex") {
  const workspace: Workspace & { setup: { ready: boolean; state: string; configuration_code: string; code?: string; attempt?: { session_id: string; account_id: string; state: string; process: string; stop_requested: boolean; recovery_reason?: string; created_at: string; ended_at?: string } } } = { id: "ws-setup", name: "Incomplete project", root: "/tmp/incomplete", profile: false,
    setup: { ready: false, state: "missing", configuration_code: "profile_missing" } };
  const workspaces = existing ? [workspace] : [];
  const requests: { path: string; init?: RequestInit }[] = [];
  const account = readySetupAccount(adapter);
  const accounts: Account[] = [account];
  const squads: Squad[] = [];
  const controls: { workspaceResponse?: Promise<Response>; initResponse?: Promise<Response> } = {};
  const result = { status: 503, message: "synthetic stop after serialization", stopStatus: 200, refreshStatus: 200 };
  const sockets: { url: string; close: ReturnType<typeof vi.fn> }[] = [];
  vi.stubGlobal("WebSocket", class {
    close = vi.fn();
    constructor(public url: string) { sockets.push(this); }
  });
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    requests.push({ path, init });
    if (path === "/v0/workspaces" && init?.method === "POST") {
      workspaces.push(workspace);
      return Response.json(workspace);
    }
    if (path === "/v0/workspaces/pick-root") return Response.json({ path: "/tmp/incomplete" });
    if (path === "/v0/workspaces" && controls.workspaceResponse) {
      const response = controls.workspaceResponse;
      delete controls.workspaceResponse;
      return response;
    }
    if (path === "/v0/workspaces") return result.refreshStatus === 200 ? Response.json(workspaces) : Response.json({ error: "refresh unavailable" }, { status: result.refreshStatus });
    if (path === "/v0/accounts") return Response.json(accounts);
    if (path === "/v0/squads") return Response.json(squads);
    if (path.endsWith("/stop")) {
      if (result.stopStatus !== 200) return Response.json({ error: "stop failed" }, { status: result.stopStatus });
      if (workspace.setup.attempt) workspace.setup.attempt.stop_requested = true;
      return Response.json({ ok: true });
    }
    if (path.endsWith("/init")) {
      if (controls.initResponse) return controls.initResponse;
      if (result.status !== 200) return Response.json({ error: result.message }, { status: result.status });
      workspace.setup.attempt = { session_id: "init-new", account_id: account.id, state: "running", process: "running", stop_requested: false, created_at: "2026-10-04T13:00:00Z" };
      return Response.json({ id: "init-new", workspace_id: workspace.id, account_id: account.id, process: "running", work: "init", owner: "web", created_at: "2026-10-04T13:00:00Z" });
    }
    if (path.startsWith("/v0/watch")) return Response.json({ available: false });
    if (path === "/v0/skills") return Response.json({ skills: [] });
    return Response.json([]);
  });
  return { requests, workspace, workspaces, accounts, account, result, sockets, squads, controls };
}

async function confirmSetup(user: ReturnType<typeof userEvent.setup>, accountID = "codex-a") {
  for (const [name, value] of [["Stack", confirmedConfig.stack], ["Methodology", confirmedConfig.methodology], ["Test command", confirmedConfig.test],
    ["Build command", confirmedConfig.build], ["Install command", confirmedConfig.install], ["Template", confirmedConfig.template]]) {
    const input = screen.getByRole("textbox", { name });
    await user.clear(input);
    await user.type(input, value);
  }
  await user.selectOptions(screen.getByRole("combobox", { name: "Execution" }), "sequential");
  await user.selectOptions(screen.getByRole("combobox", { name: "Worktree" }), "medium+");
  await user.selectOptions(screen.getByRole("combobox", { name: "Setup account" }), accountID);
  await user.type(screen.getByRole("textbox", { name: "Additional notes" }), "Keep existing content");
  await user.click(screen.getByRole("checkbox", { name: "I confirm these values and this account" }));
}

test("new incomplete workspace confirms and serializes the complete init contract", async () => {
  const { requests } = setupAPI(false);
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "New workspace" }));
  const dialog = screen.getByRole("dialog", { name: "New workspace" });
  await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "Incomplete project");
  await user.click(within(dialog).getByRole("button", { name: "Choose folder" }));
  await user.click(within(dialog).getByRole("button", { name: "Create" }));
  await screen.findByRole("heading", { name: "Set up Batuta" });
  await confirmSetup(user);
  await user.click(screen.getByRole("button", { name: "Start setup" }));
  const request = requests.find(({ path }) => path.endsWith("/init"));
  expect(JSON.parse(String(request?.init?.body))).toEqual({ account_id: "codex-a", config: confirmedConfig, brief: "Keep existing content", owner: "web", client: expect.stringMatching(/^web-/) });
});

test("existing incomplete workspace can reopen setup from normal navigation", async () => {
  setupAPI();
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "Set up Batuta" }));
  expect(await screen.findByRole("heading", { name: "Set up Batuta" })).toBeVisible();
  expect(screen.getByRole("combobox", { name: "Setup account" })).toHaveValue("");
});

async function openSetup() {
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "Set up Batuta" }));
  return user;
}

function runningAttempt() {
  return { session_id: "init-original", account_id: "original-account", state: "running", process: "running", stop_requested: false, created_at: "2026-10-04T12:00:00Z" };
}

test.each(["missing", "partial", "interrupted", "cancelled"])("opens existing %s configuration through mission creation", async state => {
  const { workspace } = setupAPI();
  workspace.setup.state = state;
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click((await screen.findAllByRole("button", { name: "Missions" }))[0]);
  await user.click(screen.getByRole("button", { name: "New mission" }));
  expect(await screen.findByRole("heading", { name: "Set up Batuta" })).toBeVisible();
});

test.each(["codex", "claude"] as const)("requires confirmation and sends literal commands using the chosen %s account", async adapter => {
  const { requests, account } = setupAPI(true, adapter);
  const user = await openSetup();
  const start = screen.getByRole("button", { name: "Start setup" });
  expect(start).toBeDisabled();
  await confirmSetup(user, account.id);
  const build = screen.getByRole("textbox", { name: "Build command" });
  await user.clear(build);
  expect(start).toBeDisabled();
  expect(screen.getByRole("checkbox")).not.toBeChecked();
  await user.type(build, "echo $(literal) && npm run build");
  expect(start).toBeDisabled();
  await user.click(screen.getByRole("checkbox"));
  await user.click(start);
  const init = requests.filter(r => r.path.endsWith("/init"));
  expect(init).toHaveLength(1);
  expect(JSON.parse(String(init[0].init?.body))).toEqual({ account_id: account.id, config: { ...confirmedConfig, build: "echo $(literal) && npm run build" }, brief: "Keep existing content", owner: "web", client: expect.stringMatching(/^web-/) });
});

test("actual Claude catalog readiness enables only explicitly confirmed init with exact selected identity and inputs", async () => {
  const { requests, accounts, account } = setupAPI(true, "claude");
  accounts.unshift(readySetupAccount("codex"));
  const user = await openSetup();
  const select = screen.getByRole("combobox", { name: "Setup account" });
  const start = screen.getByRole("button", { name: "Start setup" });
  expect(select).toHaveValue("");
  expect(start).toBeDisabled();
  await confirmSetup(user, account.id);
  expect(select).toHaveValue("claude-a");
  expect(screen.getByRole("checkbox", { name: "I confirm these values and this account" })).toBeChecked();
  expect(requests.filter(r => r.init?.method === "POST")).toHaveLength(0);
  expect(start).toBeEnabled();
  await user.click(start);
  const posts = requests.filter(r => r.init?.method === "POST");
  expect(posts).toHaveLength(1);
  expect(posts[0].path).toBe("/v0/workspaces/ws-setup/init");
  expect(initialWebClient).toMatch(/^web-/);
  expect(posts[0].init?.body).toBe(JSON.stringify({ config: confirmedConfig, account_id: "claude-a", brief: "Keep existing content", owner: "web", client: initialWebClient }));
  expect(await screen.findByRole("alert")).toHaveTextContent("synthetic stop after serialization");
  expect(select).toHaveValue("claude-a");
  expect(screen.getByRole("textbox", { name: "Additional notes" })).toHaveValue("Keep existing content");
});

test.each(["missing", "disconnected", "unknown", "expired", "incompatible", "unsupported"])("blocks %s readiness without choosing a different account", async reason => {
  const { account, accounts, requests } = setupAPI();
  const user = await openSetup();
  await confirmSetup(user);
  if (reason === "missing") accounts.splice(0);
  else {
    account.authed = false;
    account.readiness.ready = false;
    account.readiness.authenticated = false;
    account.readiness.code = reason === "expired" ? "credential_expired" : "provider_unavailable";
    if (reason === "expired") account.readiness.credential_source = "";
    if (reason === "disconnected" || reason === "incompatible" || reason === "unsupported") {
      account.readiness.cli_compatible = false;
      account.readiness.credential_configured = false;
      account.readiness.credential_source = "";
    }
    if (reason === "disconnected") {
      account.connected = false;
      account.readiness.connected = false;
      account.readiness.code = "account_disconnected";
    }
    if (reason === "incompatible") account.readiness.code = "cli_incompatible";
    if (reason === "unsupported") {
      Object.assign(account, { adapter: "gemini", provider: "Google", isolation: "unsupported", session_cap: 1, auth_method: "cli_login" });
      account.readiness.supported = false;
      account.readiness.available = false;
      account.readiness.code = "adapter_unsupported";
    }
  }
  accounts.push({ ...readySetupAccount("claude"), id: "other-ready", label: "Other identity" });
  await user.click(screen.getByRole("button", { name: "Refresh setup" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Start setup" })).toBeDisabled());
  expect(screen.getByRole("combobox", { name: "Setup account" })).toHaveValue("codex-a");
  expect(screen.getByText(/Check this account/)).toBeVisible();
  expect(requests.filter(r => r.path.endsWith("/init"))).toHaveLength(0);
});

test("observes the exact running attempt, reconnects on reopen and never stops on navigation", async () => {
  const { workspace, requests, sockets } = setupAPI();
  workspace.setup.attempt = runningAttempt();
  const user = await openSetup();
  expect(screen.getByText("init-original")).toBeVisible();
  expect(screen.getByText("original-account")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Observe terminal" }));
  await waitFor(() => expect(sockets.filter(s => s.url.includes("/init-original/terminal"))).toHaveLength(1));
  expect(requests.filter(r => r.init?.method === "POST")).toHaveLength(0);
  await user.click(screen.getByRole("button", { name: "Back home" }));
  expect(sockets.find(s => s.url.includes("/init-original/terminal"))?.close).toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Set up Batuta" }));
  await user.click(screen.getByRole("button", { name: "Observe terminal" }));
  expect(sockets.filter(s => s.url.includes("/init-original/terminal"))).toHaveLength(2);
  expect(requests.filter(r => r.path.endsWith("/stop"))).toHaveLength(0);
});

test.each([200, 500])("explicit cancellation targets the exact session and preserves stop response %s", async status => {
  const { workspace, requests, result } = setupAPI();
  workspace.setup.attempt = runningAttempt();
  result.stopStatus = status;
  const user = await openSetup();
  await user.click(screen.getByRole("button", { name: "Cancel setup attempt" }));
  await waitFor(() => expect(requests.filter(r => r.path.endsWith("/stop"))).toHaveLength(1));
  expect(requests.find(r => r.path.endsWith("/stop"))?.path).toBe("/v0/sessions/init-original/stop");
  if (status === 500) expect(await screen.findByRole("alert")).toHaveTextContent("stop failed");
  else expect(await screen.findByText(/Cancellation requested; this does not establish/)).toBeVisible();
  expect(screen.getByText("init-original")).toBeVisible();
});

test.each([409, 503, 500])("init error %s preserves the original attempt, edits and selected identity", async status => {
  const { workspace, result, requests } = setupAPI();
  workspace.setup.attempt = runningAttempt();
  result.status = status;
  result.message = `backend diagnostic ${status}`;
  const user = await openSetup();
  await confirmSetup(user);
  await user.click(screen.getByRole("button", { name: "Start setup" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(`backend diagnostic ${status}`);
  expect(screen.getByText("init-original")).toBeVisible();
  expect(screen.getByRole("textbox", { name: "Stack" })).toHaveValue("React");
  expect(screen.getByRole("combobox", { name: "Setup account" })).toHaveValue("codex-a");
  expect(requests.filter(r => r.path.endsWith("/init"))).toHaveLength(1);
});

test("incomplete exit stays recoverable, retains drafts on navigation and retries only after confirmation", async () => {
  const { workspace, result } = setupAPI();
  workspace.setup.attempt = { ...runningAttempt(), state: "exited_incomplete", process: "exited" };
  const user = await openSetup();
  expect(screen.getByText(/Termination outcome is unknown/)).toBeVisible();
  await confirmSetup(user);
  await user.click(screen.getByRole("button", { name: "Back home" }));
  await user.click(screen.getByRole("button", { name: "Set up Batuta" }));
  expect(screen.getByRole("textbox", { name: "Stack" })).toHaveValue("React");
  expect(screen.getByRole("checkbox")).not.toBeChecked();
  result.status = 200;
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Start setup" }));
  expect(await screen.findByText("init-new")).toBeVisible();
  expect(screen.queryByText("init-original")).not.toBeInTheDocument();
});

test("ready files remain ready independently of interrupted and cancellation history", async () => {
  const { workspace } = setupAPI();
  workspace.setup.ready = true;
  workspace.profile = true;
  workspace.setup.state = "ready";
  workspace.setup.configuration_code = "";
  workspace.setup.attempt = { ...runningAttempt(), process: "unknown", state: "interrupted", stop_requested: true, recovery_reason: "identity_unverified" };
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "Setup details" }));
  expect(screen.getByText("Configuration ready")).toBeVisible();
  expect(screen.getByText("Interrupted attempt")).toBeVisible();
  expect(screen.getByText(/Cancellation requested; this does not establish/)).toBeVisible();
  expect(screen.getByText("identity_unverified")).toBeVisible();
  expect(screen.queryByRole("button", { name: "Start setup" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Observe terminal" })).not.toBeInTheDocument();
});

test("Portuguese setup includes confirmation and all required fields", async () => {
  setupAPI();
  setLang("pt");
  try {
    const user = userEvent.setup();
    render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
    await user.click(await screen.findByRole("button", { name: "Configurar o Batuta" }));
    for (const name of ["Stack", "Metodologia", "Comando de teste", "Comando de build", "Comando de instalação", "Template", "Notas adicionais"]) expect(screen.getByRole("textbox", { name })).toBeVisible();
    for (const name of ["Execução", "Worktree", "Conta para configuração"]) expect(screen.getByRole("combobox", { name })).toBeVisible();
    expect(screen.getByRole("checkbox", { name: "Confirmo estes valores e esta conta" })).toBeVisible();
  } finally { setLang("en"); }
});


test.each([
  ["Stack", "   "], ["Stack", "é".repeat(4097)], ["Test command", "bad\0command"], ["Additional notes", "é".repeat(32769)], ["Additional notes", "bad\0notes"],
])("invalid %s prevents launch", async (name, value) => {
  const { requests } = setupAPI();
  const user = await openSetup();
  await confirmSetup(user);
  fireEvent.change(screen.getByRole("textbox", { name }), { target: { value } });
  await user.click(screen.getByRole("checkbox"));
  expect(screen.getByRole("button", { name: "Start setup" })).toBeDisabled();
  expect(requests.filter(r => r.path.endsWith("/init"))).toHaveLength(0);
});

test("all enum options and boundary-sized UTF-8 values serialize unchanged", async () => {
  const { requests } = setupAPI();
  const user = await openSetup();
  await confirmSetup(user);
  const stack = "é".repeat(4096);
  const brief = "é".repeat(32768);
  fireEvent.change(screen.getByRole("textbox", { name: "Stack" }), { target: { value: stack } });
  fireEvent.change(screen.getByRole("textbox", { name: "Additional notes" }), { target: { value: brief } });
  await user.selectOptions(screen.getByRole("combobox", { name: "Execution" }), "parallel");
  await user.selectOptions(screen.getByRole("combobox", { name: "Worktree" }), "off");
  await user.selectOptions(screen.getByRole("combobox", { name: "Worktree" }), "always");
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Start setup" }));
  expect(JSON.parse(String(requests.find(r => r.path.endsWith("/init"))?.init?.body))).toMatchObject({ config: { ...confirmedConfig, stack, execution: "parallel", worktree: "always" }, brief });
});

test("accepted launch remains identifiable when the subsequent refresh fails", async () => {
  const { result } = setupAPI();
  const user = await openSetup();
  await confirmSetup(user);
  result.status = 200;
  result.refreshStatus = 500;
  await user.click(screen.getByRole("button", { name: "Start setup" }));
  expect(await screen.findByText("init-new")).toBeVisible();
  expect(screen.getByRole("button", { name: "Cancel setup attempt" })).toBeEnabled();
  expect(screen.getByText(/refresh unavailable/)).toBeVisible();
});

test("refresh and navigation keep attempts and drafts scoped to their workspace", async () => {
  const { workspace, workspaces, requests } = setupAPI();
  workspace.setup.attempt = runningAttempt();
  workspaces.push({ ...workspace, id: "ws-other", name: "Other project", setup: { ...workspace.setup, attempt: { ...runningAttempt(), session_id: "other-session", account_id: "other-account" } } });
  const user = await openSetup();
  await confirmSetup(user);
  await user.click(screen.getByRole("button", { name: "Back home" }));
  await user.click(within(screen.getByRole("complementary", { name: "Batuta Console" })).getByRole("button", { name: /Incomplete project/ }));
  await user.click(screen.getByRole("button", { name: "Other project/tmp/incomplete" }));
  await user.click(screen.getByRole("button", { name: "Set up Batuta" }));
  await user.click(screen.getByRole("button", { name: "Refresh setup" }));
  expect(screen.getByText("other-session")).toBeVisible();
  expect(screen.queryByText("init-original")).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "Stack" })).toHaveValue("");
  expect(screen.getByRole("combobox", { name: "Setup account" })).toHaveValue("");
  await user.click(screen.getByRole("button", { name: "Cancel setup attempt" }));
  expect(requests.filter(r => r.path.endsWith("/stop")).map(r => r.path)).toEqual(["/v0/sessions/other-session/stop"]);
});


test("accepted cancellation remains visible when refresh fails", async () => {
  const { workspace, result } = setupAPI();
  workspace.setup.attempt = runningAttempt();
  const user = await openSetup();
  result.refreshStatus = 500;
  await user.click(screen.getByRole("button", { name: "Cancel setup attempt" }));
  expect(await screen.findByText(/Cancellation requested; this does not establish/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Cancel setup attempt" })).toBeDisabled();
});

test("absent readiness cannot inherit the account's old authenticated flag", async () => {
  const { accounts, account, requests } = setupAPI();
  accounts.splice(0, 1, { id: account.id, label: account.label, adapter: "codex", provider: "openai", connected: true, authed: true, isolation: "supported" });
  const user = await openSetup();
  await confirmSetup(user);
  expect(screen.getByRole("button", { name: "Start setup" })).toBeDisabled();
  expect(requests.filter(r => r.path.endsWith("/init"))).toHaveLength(0);
});

test("ready configuration retains normal mission creation even with historic attempts", async () => {
  const { workspace, accounts, squads } = setupAPI();
  workspace.profile = false;
  workspace.setup.ready = true;
  workspace.setup.state = "ready";
  workspace.setup.attempt = { ...runningAttempt(), state: "interrupted", process: "unknown" };
  accounts[0].authed = true;
  squads.push({ id: "ready-squad", name: "Ready squad", version: 1, draft: false, origin: "custom", members: [{ role: "lead", cli: "codex", account_id: "codex-a", maestro: true, skills: ["batuta"] }] });
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click((await screen.findAllByRole("button", { name: "Missions" }))[0]);
  await user.click(screen.getByRole("button", { name: "New mission" }));
  expect(await screen.findByRole("dialog", { name: "New mission" })).toBeVisible();
  expect(screen.queryByRole("heading", { name: "Set up Batuta" })).not.toBeInTheDocument();
});


test("an older refresh cannot overwrite an accepted attempt after navigating away", async () => {
  const { workspace, workspaces, controls, result } = setupAPI();
  workspaces.push({ ...workspace, id: "ws-other", name: "Other project", setup: { ...workspace.setup, attempt: { ...runningAttempt(), session_id: "other-session" } } });
  const user = await openSetup();
  await confirmSetup(user);
  const stale = Response.json(workspaces);
  let resolveWorkspace: (response: Response) => void = () => { throw new Error("deferred response was not initialized"); };
  let resolveInit: (response: Response) => void = () => { throw new Error("deferred response was not initialized"); };
  controls.initResponse = new Promise(resolve => { resolveInit = resolve; });
  await user.click(screen.getByRole("button", { name: "Start setup" }));
  controls.workspaceResponse = new Promise(resolve => { resolveWorkspace = resolve; });
  await user.click(screen.getByRole("button", { name: "Back home" }));
  await user.click(within(screen.getByRole("complementary", { name: "Batuta Console" })).getByRole("button", { name: /Incomplete project/ }));
  await user.click(screen.getByRole("button", { name: "Other project/tmp/incomplete" }));
  await user.click(screen.getByRole("button", { name: "Set up Batuta" }));
  workspace.setup.attempt = { ...runningAttempt(), session_id: "late-accepted", account_id: "codex-a" };
  await act(async () => { resolveInit(Response.json({ id: "late-accepted", workspace_id: workspace.id, account_id: "codex-a", process: "running", created_at: "2026-10-04T13:00:00Z" })); });
  expect(screen.getByText("other-session")).toBeVisible();
  result.refreshStatus = 500;
  await act(async () => { resolveWorkspace(stale); });
  await user.click(screen.getByRole("button", { name: "Back home" }));
  await user.click(within(screen.getByRole("complementary", { name: "Batuta Console" })).getByRole("button", { name: /Other project/ }));
  await user.click(screen.getByRole("button", { name: "Incomplete project/tmp/incomplete" }));
  await user.click(screen.getByRole("button", { name: "Set up Batuta" }));
  expect(screen.getByText("late-accepted")).toBeVisible();
  expect(screen.getByRole("textbox", { name: "Stack" })).toHaveValue("React");
  expect(screen.getByRole("combobox", { name: "Setup account" })).toHaveValue("codex-a");
});

test("ready configuration with historical stop intent does not instruct the user to retry", async () => {
  const { workspace } = setupAPI();
  workspace.setup.ready = true;
  workspace.setup.state = "ready";
  workspace.setup.attempt = { ...runningAttempt(), process: "exited", state: "cancel_requested", stop_requested: true };
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click(await screen.findByRole("button", { name: "Setup details" }));
  expect(screen.getByText("Configuration ready")).toBeVisible();
  expect(screen.getByText(/Termination outcome is unknown/)).toBeVisible();
  expect(screen.queryByText(/Confirm the inputs and account to retry/)).not.toBeInTheDocument();
});
