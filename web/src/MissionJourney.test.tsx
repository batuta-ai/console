/// <reference types="node" />
import { createHash, webcrypto } from "node:crypto";
import { StrictMode } from "react";
import { act, render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider } from "@lyra-ds/react/theme-provider";
import { expect, test, vi } from "vitest";
import { App } from "./App";
import type { ExecutionState, Member, Mission, PlanApproval, PlanDetail, Session, Watch } from "./lib";

const members: Member[] = [
  { id: "worker", role: "Builder", cli: "codex", account_id: "builder", model: "builder-model", effort: "low" },
  { id: "lead", role: "Conductor", maestro: true, skills: ["batuta"], cli: "claude", account_id: "planner", model: "planner-model", effort: "high" },
];

function journeyAPI() {
  const mission: Mission = { id: "m1", title: "Exact journey", brief: "Preserve the objective", workspace_id: "w1", squad_id: "s1", status: "open", squad_snapshot: { name: "Chosen squad", members: structuredClone(members) } };
  const missions = [mission];
  const sessions: Session[] = [];
  const requests: { path: string; init?: RequestInit }[] = [];
  const controls: { planning?: Promise<Response>; route?: (path: string, init?: RequestInit) => Response | Promise<Response> | undefined } = {};
  const sockets: { onmessage: (() => void) | null }[] = [];
  vi.stubGlobal("WebSocket", class {
    onmessage: (() => void) | null = null;
    constructor() { sockets.push(this); }
    close() {}
  });
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    requests.push({ path, init });
    const routed = controls.route?.(path, init);
    if (routed) return routed;
    if (path === "/v0/workspaces") return Response.json([{ id: "w1", name: "Workspace", root: "/tmp/workspace", setup: { ready: true, state: "ready" } }]);
    if (path === "/v0/missions" && init?.method === "POST") return Response.json({ ...mission, ...JSON.parse(String(init.body)) });
    if (path === "/v0/missions") return Response.json(missions);
    if (path === "/v0/squads") return Response.json([{ id: "s1", name: "Chosen squad", version: 1, draft: false, members }]);
    if (path === "/v0/accounts") return Response.json(members.map(m => ({ id: m.account_id, label: m.account_id, adapter: m.cli, connected: true, authed: true })));
    if (path === "/v0/skills") return Response.json({ root: "/skills", skills: [] });
    if (path === "/v0/sessions" && init?.method === "POST") {
      if (controls.planning) return controls.planning;
      const session = { id: "planning", mission_id: "m1", process: "running", owner: "web", adapter: "claude", batuta_injected: true };
      sessions.push(session);
      return Response.json(session);
    }
    if (path === "/v0/sessions") return Response.json(sessions);
    if (path.startsWith("/v0/watch")) return Response.json({ available: true, delivery: "FOREIGN", header: { state: "done" }, attention: { text: "FOREIGN QUESTION" } });
    return Response.json([]);
  });
  return { mission, missions, sessions, requests, controls, sockets };
}

async function openMission() {
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click((await screen.findAllByRole("button", { name: "Missions" }))[0]);
  await user.click(await screen.findByRole("button", { name: /Exact journey/ }));
  return user;
}

const mutations = (requests: { path: string; init?: RequestInit }[]) => requests.filter(r => r.init?.method && r.init.method !== "GET");

test("planning always uses the one explicit maestro and shows its fixed binding", async () => {
  const { requests } = journeyAPI();
  const user = await openMission();
  expect(screen.getByText(/planner-model.*high/)).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Plan with Conductor" }));
  expect(JSON.parse(String(mutations(requests)[0]?.init?.body))).toEqual({ mission_id: "m1", member_id: "lead", owner: "web", client: expect.any(String), batuta: true });
  expect(mutations(requests)).toHaveLength(1);
});

test.each([[], [{ ...members[1], id: undefined }], [members[1], { ...members[1], id: "other" }]].map(invalid => [invalid]))("missing or ambiguous maestro cannot plan: %j", async (invalid) => {
  const { mission, requests } = journeyAPI();
  mission.squad_snapshot = { members: invalid };
  await openMission();
  expect(screen.getByText(/Choose exactly one maestro with a member ID/)).toBeVisible();
  expect(screen.getByRole("button", { name: /Plan with/ })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(0);
});

test("pending and lost planning responses block duplicate starts until explicit inspection", async () => {
  const { requests, controls } = journeyAPI();
  let reject: (reason: Error) => void = () => { throw Error("not initialized"); };
  controls.planning = new Promise((_, fail) => { reject = fail; });
  const user = await openMission();
  const start = screen.getByRole("button", { name: "Plan with Conductor" });
  await user.dblClick(start);
  expect(start).toBeDisabled();
  expect(mutations(requests)).toHaveLength(1);
  await act(async () => { reject(Error("lost planning response")); });
  expect(await screen.findByText(/lost planning response/)).toBeVisible();
  expect(start).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Inspect planning sessions" }));
  await waitFor(() => expect(start).toBeEnabled());
  expect(mutations(requests)).toHaveLength(1);
});

test("mission creation preserves workspace, squad and multiline objective", async () => {
  const { requests } = journeyAPI();
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click((await screen.findAllByRole("button", { name: "Missions" }))[0]);
  await screen.findByRole("button", { name: /Exact journey/ });
  await user.click(screen.getByRole("button", { name: "New mission" }));
  const dialog = screen.getByRole("dialog");
  await user.type(within(dialog).getByRole("textbox", { name: /What is this mission/ }), "First line\nSecond line");
  await user.click(within(dialog).getByRole("button", { name: "Create" }));
  expect(JSON.parse(String(mutations(requests)[0]?.init?.body))).toEqual({ workspace_id: "w1", title: "First line", brief: "First line\nSecond line", squad_id: "s1" });
});


function nativeAPI(bound = true) {
  vi.stubGlobal("crypto", webcrypto);
  const api = journeyAPI();
  const content = "# Plan — Café 🌿\r\n**Status:** proposed\r\n## Tasks\r\n- [ ] 1. First\r\n  Scope: web/src/**\r\n  Accept: exact bytes\r\n\r\nTrailing context  \r\n";
  const identity = { mission_id: "m1", binding: { workspace_id: "w1", workspace_root: "/tmp/workspace", canonical_root: "/tmp/workspace", plan_path: ".batuta/plans/exact.md", plan_slug: "exact", plan_digest: "task-set-digest" }, version: createHash("sha256").update(content).digest("hex") };
  const plan: PlanDetail = { identity, content: Buffer.from(content).toString("base64"), title: "Exact plan", goal: "Exact bytes", context: "Full context", created: "today", inputs: "brief", status: "proposed", tasks: [{ id: "task_1", number: 1, title: "First", status: "pending", domain: "frontend", complexity: "high", dependencies: ["task_0"], scope: ["web/src/**"], accept: ["exact bytes"], executor: "codex", model: "synthetic", line: 4 }] };
  const receipt: PlanApproval = { id: "receipt-1", identity, content: plan.content, approved_at: "2026-10-06T12:00:00Z" };
  const question = { mission_id: "m1", workspace_id: "w1", delivery: "delivery-1", task: "task_1", execution: 3, question_id: "question-1", opened_digest: "opened-1" };
  const watch: Watch = { available: true, source: "current", scope: "mission", mission_id: "m1", workspace_id: "w1", delivery: "delivery-1", header: { state: "waiting_input" }, pending_question: { target: question, prompt: "Which exact output?" }, progression: { delivery: "delivery-1", required: false, cleared: false } };
  const state: ExecutionState = { target: identity, approval_id: receipt.id, delivery: "delivery-1", active: false, bound: false };
  if (bound) api.mission.binding = identity.binding;
  const control: { override?: (path: string, init?: RequestInit) => Response | Promise<Response> | undefined } = {};
  api.controls.route = (path, init) => {
    const override = control.override?.(path, init);
    if (override) return override;
    if (path === "/v0/missions/m1/binding") {
      api.mission.binding = identity.binding;
      return Response.json(api.mission);
    }
    if (path === "/v0/missions/m1/plan?workspace_id=w1") return Response.json(plan);
    if (path === "/v0/missions/m1/plan/approvals/receipt-1?workspace_id=w1") return Response.json(receipt);
    if (path === "/v0/missions/m1/plan/approvals") {
      plan.approval = receipt;
      return Response.json({ target: identity, effect: "recorded", receipt });
    }
    if (path === "/v0/missions/m1/execution?workspace_id=w1&approval_id=receipt-1") return Response.json(state);
    if (path === "/v0/missions/m1/execution" || path.endsWith("/execution/resume")) {
      Object.assign(state, { bound: true, opened_digest: "opened-1", watch, gate: watch.progression });
      return Response.json({ target: identity, approval_id: receipt.id, effect: "started", delivery: state.delivery, opened_digest: state.opened_digest }, { status: 202 });
    }
    if (path === "/v0/watch/answer") {
      delete watch.pending_question;
      watch.header = { state: "canceled" };
      return Response.json({ target: question, delivery: state.delivery, effect: "recorded" });
    }
    return undefined;
  };
  return { ...api, content, identity, plan, receipt, question, watch, state, control };
}

async function approve(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "Approve displayed bytes" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Start native execution" })).toBeEnabled());
}

test("binds an explicit artifact and approves the complete original bytes and identity", async () => {
  const { requests, content, identity, plan } = nativeAPI(false);
  const user = await openMission();
  expect(screen.queryByRole("button", { name: "Approve displayed bytes" })).not.toBeInTheDocument();
  await user.type(screen.getByRole("textbox", { name: "Plan path" }), identity.binding.plan_path);
  await user.click(screen.getByRole("button", { name: "Bind plan" }));
  await screen.findByText("Exact plan");
  expect(screen.getByLabelText("Original plan content").textContent).toBe(content);
  expect(screen.getByRole("link", { name: "Download original bytes" })).toHaveAttribute("href", `data:application/octet-stream;base64,${plan.content}`);
  expect(screen.getByText("task_0")).toBeVisible();
  expect(screen.getByText("web/src/**")).toBeVisible();
  expect(screen.getByText("exact bytes")).toBeVisible();
  expect(screen.getByText(identity.version)).toBeVisible();
  await approve(user);
  expect(mutations(requests).map(r => [r.path, JSON.parse(String(r.init?.body))])).toEqual([
    ["/v0/missions/m1/binding", { workspace_id: "w1", plan_path: identity.binding.plan_path }],
    ["/v0/missions/m1/plan/approvals", identity],
  ]);
  expect(screen.getByText("receipt-1")).toBeVisible();
  expect(screen.queryByText("FOREIGN QUESTION")).not.toBeInTheDocument();
});

test.each(["base64", "utf8", "digest", "foreign", "receipt"])("malformed %s plan or receipt never enables approval or execution", async kind => {
  const { plan, receipt } = nativeAPI();
  if (kind === "base64") plan.content = "%%%";
  if (kind === "utf8") {
    plan.content = "/w==";
    plan.identity.version = createHash("sha256").update(Buffer.from([255])).digest("hex");
  }
  if (kind === "digest") plan.identity.version = "0".repeat(64);
  if (kind === "foreign") plan.identity.binding.workspace_id = "other";
  if (kind === "receipt") plan.approval = { ...receipt, content: "d3Jvbmc=" };
  await openMission();
  expect(await screen.findByText(/Plan or receipt could not be verified/)).toBeVisible();
  expect(screen.queryByRole("button", { name: "Approve displayed bytes" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Start native execution" })).not.toBeInTheDocument();
});

test("native journey uses receipt and identified question, then explicitly resumes the same opening", async () => {
  const { requests, identity, question, receipt, state } = nativeAPI();
  const user = await openMission();
  expect(await screen.findByRole("button", { name: "Start native execution" })).toBeDisabled();
  await approve(user);
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText("Which exact output?");
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeDisabled();
  await user.type(screen.getByRole("textbox", { name: "Answer this question" }), "Use approved bytes");
  await user.click(screen.getByRole("button", { name: "Send reply" }));
  expect(await screen.findByText(/Answer recorded. Resume is a separate action/)).toBeVisible();
  expect(mutations(requests).filter(r => r.path.endsWith("/resume"))).toHaveLength(0);
  await waitFor(() => expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeEnabled());
  state.active = true;
  await user.click(screen.getByRole("button", { name: "Resume this delivery" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeDisabled());
  expect(mutations(requests).map(r => [r.path, JSON.parse(String(r.init?.body))])).toEqual([
    ["/v0/missions/m1/plan/approvals", identity],
    ["/v0/missions/m1/execution", { target: identity, approval_id: receipt.id }],
    ["/v0/watch/answer", { ...question, text: "Use approved bytes" }],
    ["/v0/missions/m1/execution/resume", { target: identity, approval_id: receipt.id, delivery: "delivery-1", opened_digest: "opened-1" }],
  ]);
});

test.each(["lost", "uncertain", "rejected"])("%s native start cannot replay before inspection", async kind => {
  const { control, requests, identity, receipt } = nativeAPI();
  control.override = (path) => {
    if (path !== "/v0/missions/m1/execution") return;
    if (kind === "lost") return Promise.reject(Error("lost response"));
    return Response.json({ target: identity, approval_id: receipt.id, effect: kind, error: { code: "synthetic", message: "native unavailable" } }, { status: 503 });
  };
  const user = await openMission();
  await approve(user);
  await user.dblClick(screen.getByRole("button", { name: "Start native execution" }));
  expect(await screen.findByText(/Inspect the exact target before retrying/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Inspect mission state" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Start native execution" })).toBeEnabled());
  expect(mutations(requests).filter(r => r.path.endsWith("/execution"))).toHaveLength(1);
});

test("restart reads the original receipt from the bound snapshot after source deletion", async () => {
  const { plan, receipt, state, watch, mission, requests } = nativeAPI();
  plan.approval = receipt;
  mission.binding = { ...receipt.identity.binding, delivery_id: "delivery-1", opened_digest: "opened-1" };
  delete watch.pending_question;
  watch.header = { state: "canceled" };
  Object.assign(state, { bound: true, opened_digest: "opened-1", watch, gate: watch.progression });
  const user = await openMission();
  await waitFor(() => expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Resume this delivery" }));
  expect(JSON.parse(String(mutations(requests)[0]?.init?.body))).toEqual({ target: receipt.identity, approval_id: receipt.id, delivery: "delivery-1", opened_digest: "opened-1" });
  expect(mutations(requests).some(r => r.path.endsWith("approvals"))).toBe(false);
});

test.each([
  ["done", true, false, "Review blocked"],
  ["done", true, true, "Completed"],
  ["running", false, false, "Running"],
  ["waiting_input", false, false, "Waiting"],
  ["review_blocked", true, false, "Review blocked"],
])("backend %s and gate (%s/%s) determine status", async (backend, required, cleared, label) => {
  const { plan, receipt, state, watch } = nativeAPI();
  plan.approval = receipt;
  delete watch.pending_question;
  watch.header = { state: backend };
  watch.progression = { delivery: "delivery-1", required, cleared };
  Object.assign(state, { bound: true, opened_digest: "opened-1", watch, gate: watch.progression });
  await openMission();
  await waitFor(() => expect(screen.getByLabelText("Mission execution status")).toHaveTextContent(label));
});

test.each(["source", "gate", "question", "target"])("foreign or unavailable %s never permits a completion or mutation", async kind => {
  const { plan, receipt, state, watch } = nativeAPI();
  plan.approval = receipt;
  watch.header = { state: "done" };
  Object.assign(state, { bound: true, opened_digest: "opened-1", watch, gate: watch.progression });
  if (kind === "source") watch.source = "unavailable";
  if (kind === "gate") state.gate = { delivery: "foreign", required: false, cleared: true };
  if (kind === "question" && watch.pending_question) watch.pending_question.target.mission_id = "foreign";
  if (kind === "target") state.target = { ...receipt.identity, version: "0".repeat(64) };
  await openMission();
  await screen.findByText(/Mission source, identity or supervision gate could not be verified/);
  expect(screen.getByLabelText("Mission execution status")).toHaveTextContent("Unavailable");
  expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeDisabled();
  expect(screen.queryByRole("textbox", { name: "Answer this question" })).not.toBeInTheDocument();
});

test("approval pending blocks a duplicate and a stale approval remains visibly unapproved", async () => {
  const { control, requests, identity } = nativeAPI();
  let resolve: (response: Response) => void = () => { throw Error("uninitialized"); };
  const pending = new Promise<Response>(done => { resolve = done; });
  control.override = path => path.endsWith("/plan/approvals") ? pending : undefined;
  const user = await openMission();
  const button = await screen.findByRole("button", { name: "Approve displayed bytes" });
  await user.dblClick(button);
  expect(button).toBeDisabled();
  expect(mutations(requests)).toHaveLength(1);
  await act(async () => resolve(Response.json({ target: identity, effect: "rejected", error: { code: "plan_stale", message: "plan changed after display" } }, { status: 409 })));
  expect(await screen.findByText(/plan changed after display/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
  expect(button).toBeDisabled();
});

test("an edited approved source can be inspected and approved anew before any delivery opens", async () => {
  const { plan, receipt, content, identity, control, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  const edited = content + "\r\nNew objective requiring new approval\r\n";
  plan.content = Buffer.from(edited).toString("base64");
  plan.identity = { ...identity, version: createHash("sha256").update(edited).digest("hex") };
  delete plan.approval;
  const nextReceipt = { ...receipt, id: "receipt-2", identity: plan.identity, content: plan.content };
  control.override = (path, init) => {
    if (path.endsWith("/execution") && init?.method === "POST") return Response.json({ target: receipt.identity, approval_id: receipt.id, effect: "rejected", error: { code: "plan_stale", message: "current source needs its own approval" } }, { status: 409 });
    if (path.endsWith("/plan/approvals")) return Response.json({ target: plan.identity, effect: "recorded", receipt: nextReceipt });
    if (path.includes("/plan/approvals/receipt-2?")) return Response.json(nextReceipt);
    if (path.includes("approval_id=receipt-2")) return Response.json({ target: plan.identity, approval_id: nextReceipt.id, delivery: "delivery-2", bound: false, active: false });
  };
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText(/current source needs its own approval/);
  await user.click(screen.getByRole("button", { name: "Inspect mission state" }));
  await waitFor(() => expect(screen.getByLabelText("Original plan content").textContent).toBe(edited));
  expect(screen.getByRole("button", { name: "Approve displayed bytes" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(2);
  await approve(user);
  expect(JSON.parse(String(mutations(requests)[2].init?.body))).toEqual(plan.identity);
  expect(screen.getByText("receipt-2")).toBeVisible();
  expect(receipt.identity).toEqual(identity);
  expect(JSON.parse(sessionStorage.getItem("bt-journey:w1:m1:approved:receipt-1") || "null").approval).toEqual(receipt);
  expect(JSON.parse(sessionStorage.getItem("bt-journey:w1:m1:approved:receipt-2") || "null").approval).toEqual(nextReceipt);
});

test.each(["rejected", "uncertain", "lost", "foreign"])("%s answer never resumes or automatically retries", async effect => {
  const { control, question, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText("Which exact output?");
  control.override = path => {
    if (path !== "/v0/watch/answer") return;
    if (effect === "lost") return Promise.reject(Error("answer response lost"));
    return Response.json({ target: effect === "foreign" ? { ...question, execution: 99 } : question, delivery: "delivery-1", effect: effect === "foreign" ? "recorded" : effect, error: { code: "stale", message: "answer rejected or uncertain" } }, { status: effect === "foreign" ? 200 : 409 });
  };
  await user.type(screen.getByRole("textbox", { name: "Answer this question" }), "Exact answer");
  await user.dblClick(screen.getByRole("button", { name: "Send reply" }));
  expect(await screen.findByText(/Inspect the exact target before retrying/)).toBeVisible();
  expect(screen.queryByText(/Answer recorded. Resume is a separate action/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Send reply" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeDisabled();
  expect(mutations(requests).filter(r => r.path.endsWith("/answer"))).toHaveLength(1);
  expect(mutations(requests).filter(r => r.path.endsWith("/resume"))).toHaveLength(0);
});

test.each(["rejected", "uncertain", "lost"])("%s resume preserves the same original target and requires inspection", async effect => {
  const { plan, receipt, state, watch, control, requests } = nativeAPI();
  plan.approval = receipt;
  delete watch.pending_question;
  watch.header = { state: "canceled" };
  Object.assign(state, { bound: true, opened_digest: "opened-1", watch, gate: watch.progression });
  control.override = path => {
    if (!path.endsWith("/resume")) return;
    if (effect === "lost") return Promise.reject(Error("resume response lost"));
    return Response.json({ target: receipt.identity, approval_id: receipt.id, delivery: state.delivery, opened_digest: state.opened_digest, effect, error: { code: "resume_failed", message: "resume failed" } }, { status: 503 });
  };
  const user = await openMission();
  await waitFor(() => expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeEnabled());
  await user.dblClick(screen.getByRole("button", { name: "Resume this delivery" }));
  expect(await screen.findByText(/Inspect the exact target before retrying/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(1);
  expect(JSON.parse(String(mutations(requests)[0].init?.body))).toEqual({ target: receipt.identity, approval_id: receipt.id, delivery: "delivery-1", opened_digest: "opened-1" });
});

test("switching missions discards the previous receipt and question even with a late state response", async () => {
  const { plan, receipt, missions, control, requests } = nativeAPI();
  plan.approval = receipt;
  missions.push({ id: "m2", title: "Second mission", workspace_id: "w1", status: "open", squad_snapshot: { members } });
  let resolve: (response: Response) => void = () => { throw Error("uninitialized"); };
  const late = new Promise<Response>(done => { resolve = done; });
  control.override = path => path.includes("/execution?") ? late : undefined;
  const user = await openMission();
  await screen.findByText("receipt-1");
  await user.click(screen.getByRole("button", { name: /^Missions/ }));
  await user.click(screen.getByRole("button", { name: /Second mission/ }));
  await act(async () => resolve(Response.json({ target: receipt.identity, approval_id: receipt.id, active: true, bound: false, delivery: "old-delivery" })));
  expect(screen.queryByText("receipt-1")).not.toBeInTheDocument();
  expect(screen.queryByText("old-delivery")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Start native execution" })).not.toBeInTheDocument();
  expect(mutations(requests)).toHaveLength(0);
});

test("an unavailable plan source cannot inherit completion from the catalog or workspace watch", async () => {
  const { mission, control } = nativeAPI();
  mission.status = "done";
  control.override = path => path.includes("/plan?") ? Response.json({ error: "source deleted" }, { status: 404 }) : undefined;
  await openMission();
  expect(await screen.findByText(/source deleted/)).toBeVisible();
  expect(screen.getByLabelText("Mission execution status")).toHaveTextContent("Unavailable");
  expect(screen.queryByRole("button", { name: "Start native execution" })).not.toBeInTheDocument();
});

test("a successful answer cannot be submitted twice while an old question projection remains", async () => {
  const { control, question, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText("Which exact output?");
  control.override = path => path === "/v0/watch/answer" ? Response.json({ target: question, delivery: question.delivery, effect: "recorded" }) : undefined;
  await user.type(screen.getByRole("textbox", { name: "Answer this question" }), "Recorded");
  await user.click(screen.getByRole("button", { name: "Send reply" }));
  await screen.findByText(/Answer recorded. Resume is a separate action/);
  await user.type(screen.getByRole("textbox", { name: "Answer this question" }), "Duplicate");
  expect(screen.getByRole("button", { name: "Send reply" })).toBeDisabled();
  expect(mutations(requests).filter(r => r.path.endsWith("/answer"))).toHaveLength(1);
});

test("selected mission refresh observes an explicit binding made by another client", async () => {
  const { mission, identity, sockets } = nativeAPI(false);
  await openMission();
  expect(screen.queryByRole("button", { name: "Approve displayed bytes" })).not.toBeInTheDocument();
  mission.binding = identity.binding;
  act(() => sockets[0].onmessage?.());
  expect(await screen.findByRole("button", { name: "Approve displayed bytes" })).toBeEnabled();
});

test("failed creation preserves the objective and requires catalog inspection before retry", async () => {
  const { controls, requests } = journeyAPI();
  controls.route = (path, init) => path === "/v0/missions" && init?.method === "POST" ? Response.json({ error: "creation response unavailable" }, { status: 503 }) : undefined;
  const user = userEvent.setup();
  render(<ThemeProvider defaultTheme="light"><App /></ThemeProvider>);
  await user.click((await screen.findAllByRole("button", { name: "Missions" }))[0]);
  await screen.findByRole("button", { name: /Exact journey/ });
  await user.click(screen.getByRole("button", { name: "New mission" }));
  const dialog = screen.getByRole("dialog");
  await user.type(within(dialog).getByRole("textbox", { name: /What is this mission/ }), "Keep this objective");
  await user.dblClick(within(dialog).getByRole("button", { name: "Create" }));
  expect(await within(dialog).findByText(/creation response unavailable/)).toBeVisible();
  expect(within(dialog).getByRole("textbox", { name: /What is this mission/ })).toHaveValue("Keep this objective");
  expect(within(dialog).getByRole("button", { name: "Create" })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(1);
});

test("navigation retains the original approved snapshot when the mutable source is deleted", async () => {
  const { control, content, requests, receipt, watch } = nativeAPI();
  const user = await openMission();
  await approve(user);
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText("Which exact output?");
  delete watch.pending_question;
  watch.header = { state: "canceled" };
  await user.click(screen.getByRole("button", { name: /^Missions/ }));
  control.override = path => path.includes("/plan?") ? Response.json({ error: "source deleted" }, { status: 404 }) : undefined;
  await user.click(screen.getByRole("button", { name: /Exact journey/ }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Resume this delivery" })).toBeEnabled());
  expect(screen.getByLabelText("Original plan content").textContent).toBe(content);
  await user.click(screen.getByRole("button", { name: "Resume this delivery" }));
  expect(JSON.parse(String(mutations(requests).find(r => r.path.endsWith("/resume"))?.init?.body))).toEqual({ target: receipt.identity, approval_id: receipt.id, delivery: "delivery-1", opened_digest: "opened-1" });
});

test("planning uncertainty survives leaving and reopening the mission", async () => {
  const { controls, requests } = journeyAPI();
  controls.route = (path, init) => path === "/v0/sessions" && init?.method === "POST" ? Promise.reject(Error("planning connection lost")) : undefined;
  const user = await openMission();
  await user.click(screen.getByRole("button", { name: "Plan with Conductor" }));
  await screen.findByText(/planning connection lost/);
  await user.click(screen.getByRole("button", { name: /^Missions/ }));
  await user.click(screen.getByRole("button", { name: /Exact journey/ }));
  expect(screen.getByRole("button", { name: "Plan with Conductor" })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(1);
});

test("malformed question identifiers are rejected before the pending question is rendered", async () => {
  const { plan, receipt, state, watch, control } = nativeAPI();
  plan.approval = receipt;
  Object.assign(state, { bound: true, opened_digest: "opened-1", watch, gate: watch.progression });
  control.override = path => path.includes("/execution?") ? Response.json({ ...state, watch: { ...watch, pending_question: { ...watch.pending_question, target: { ...watch.pending_question?.target, task: { wrong: "type" } } } } }) : undefined;
  await openMission();
  expect(await screen.findByText(/Mission source, identity or supervision gate could not be verified/)).toBeVisible();
  expect(screen.queryByRole("textbox", { name: "Answer this question" })).not.toBeInTheDocument();
});


test("the application StrictMode lifecycle leaves plan inspection usable", async () => {
  nativeAPI();
  const user = userEvent.setup();
  render(<StrictMode><ThemeProvider defaultTheme="light"><App /></ThemeProvider></StrictMode>);
  await user.click((await screen.findAllByRole("button", { name: "Missions" }))[0]);
  await user.click(await screen.findByRole("button", { name: /Exact journey/ }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Approve displayed bytes" })).toBeEnabled());
});

test("a changed task digest allows explicit rebinding before any execution", async () => {
  const { plan, receipt, control, mission, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  const nextBinding = { ...receipt.identity.binding, plan_path: ".batuta/plans/revised.md", plan_digest: "revised-task-digest" };
  control.override = path => {
    if (path.includes("/plan?")) return Response.json({ error: "plan binding is stale; bind the revised path" }, { status: 409 });
  };
  await user.click(screen.getByRole("button", { name: "Inspect mission state" }));
  await screen.findByText(/plan binding is stale/);
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
  const input = screen.getByRole("textbox", { name: "Plan path" });
  expect(input).toBeEnabled();
  await user.clear(input);
  await user.type(input, nextBinding.plan_path);
  plan.identity = { ...plan.identity, binding: nextBinding };
  delete plan.approval;
  control.override = path => {
    if (path.endsWith("/binding")) {
      mission.binding = nextBinding;
      return Response.json(mission);
    }
  };
  await user.click(screen.getByRole("button", { name: "Bind plan" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Approve displayed bytes" })).toBeEnabled());
  expect(JSON.parse(String(mutations(requests).at(-1)?.init?.body))).toEqual({ workspace_id: "w1", plan_path: nextBinding.plan_path });
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
});

test.each(["lost", "uncertain"])("%s initial execution preserves the original snapshot across navigation and source edits", async effect => {
  const { plan, receipt, content, control, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  control.override = path => {
    if (path !== "/v0/missions/m1/execution") return;
    if (effect === "lost") return Promise.reject(Error("lost initial execution response"));
    return Response.json({ target: receipt.identity, approval_id: receipt.id, effect: "uncertain" }, { status: 503 });
  };
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText(/Inspect the exact target before retrying/);
  const edited = content + "new source";
  plan.content = Buffer.from(edited).toString("base64");
  plan.identity = { ...plan.identity, version: createHash("sha256").update(edited).digest("hex") };
  delete plan.approval;
  await user.click(screen.getByRole("button", { name: /^Missions/ }));
  await user.click(screen.getByRole("button", { name: /Exact journey/ }));
  await user.click(await screen.findByRole("button", { name: "Inspect mission state" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Inspect mission state" })).toBeEnabled());
  expect(screen.getByLabelText("Original plan content").textContent).toBe(content);
  expect(screen.getByRole("button", { name: "Bind plan" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Approve displayed bytes" })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(2);
});

test("an opened delivery keeps its original content when the mutable source changes", async () => {
  const { plan, content, receipt, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  await user.click(screen.getByRole("button", { name: "Start native execution" }));
  await screen.findByText("Which exact output?");
  const edited = content + "new source";
  plan.content = Buffer.from(edited).toString("base64");
  plan.identity = { ...plan.identity, version: createHash("sha256").update(edited).digest("hex") };
  delete plan.approval;
  await user.click(screen.getByRole("button", { name: "Inspect mission state" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Inspect mission state" })).toBeEnabled());
  expect(screen.getByLabelText("Original plan content").textContent).toBe(content);
  expect(screen.getByText(receipt.id)).toBeVisible();
  expect(screen.getByRole("button", { name: "Bind plan" })).toBeDisabled();
  expect(mutations(requests)).toHaveLength(2);
});

test("a cached approval cannot make a deleted source eligible for first execution", async () => {
  const { control, requests } = nativeAPI();
  const user = await openMission();
  await approve(user);
  await user.click(screen.getByRole("button", { name: /^Missions/ }));
  control.override = path => path.includes("/plan?") ? Response.json({ error: "source deleted before opening" }, { status: 404 }) : undefined;
  await user.click(screen.getByRole("button", { name: /Exact journey/ }));
  await screen.findByText(/source deleted before opening/);
  expect(screen.getByRole("button", { name: "Start native execution" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Bind plan" })).toBeEnabled();
  expect(mutations(requests)).toHaveLength(1);
});
