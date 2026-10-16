import { T, localeTag, type Msg } from "./i18n";

export type Screen = "home" | "missions" | "room" | "squads" | "editor" | "accounts" | "activity" | "review" | "login" | "setup";

export type SetupConfig = {
  stack: string;
  methodology: string;
  test: string;
  build: string;
  install: string;
  execution: "" | "sequential" | "parallel";
  worktree: "" | "off" | "medium+" | "always";
  template: string;
};
export type InitAttempt = {
  session_id: string;
  account_id: string;
  state: string;
  process: string;
  stop_requested: boolean;
  recovery_reason?: string;
  created_at: string;
  ended_at?: string;
};
export type Workspace = {
  id: string;
  name: string;
  root: string;
  profile?: boolean;
  setup?: { ready: boolean; state: string; code?: string; configuration_code?: string; attempt?: InitAttempt };
};
export type SetupDraft = { config: SetupConfig; account_id: string; brief: string };
export type Member = {
  id?: string;
  role: string;
  cli: string;
  account_id: string;
  maestro?: boolean;
  model?: string;
  effort?: string;
  skills?: string[];
};
export type Squad = { id: string; name: string; purpose?: string; version: number; draft: boolean; origin?: string; members: Member[] };
export type Skill = {
  id: string;
  name: string;
  origin: string;
  summary?: string;
  installed?: boolean;
  path?: string;
};
export type Mission = {
  binding?: MissionBinding;
  id: string;
  title: string;
  brief?: string;
  status: string;
  workspace_id: string;
  attention?: string;
  squad_id?: string;
  squad_snapshot?: { name?: string; members?: Member[] };
};
export type Account = {
  id: string;
  label: string;
  provider: string;
  adapter: string;
  isolation: string;
  connected: boolean;
  authed?: boolean;
  session_cap?: number;
  vault_ref?: string;
  readiness?: {
    supported: boolean;
    available: boolean;
    cli_compatible: boolean;
    connected: boolean;
    credential_configured: boolean;
    authenticated: boolean;
    ready: boolean;
    code: string;
    checked_at: string;
    credential_source?: string;
  };
};
export type Event = { seq: number; at: string; kind: string; payload?: Record<string, unknown> };
export type Limit = {
  account_id: string;
  percent: number | null;
  source: string;
  origin?: string;
  window?: string;
  last_attempt?: string;
  observed_at?: string;
};
export type Session = {
  member_id?: string;
  id: string;
  process: string;
  owner: string;
  owner_client?: string;
  adapter: string;
  mission_id: string;
  workspace_id?: string;
  recovery_reason?: string;
  created_at?: string;
  ended_at?: string;
  account_id?: string;
  work?: string;
  batuta_injected?: boolean;
  skills_version?: string;
};
export type Watch = {
  scope?: string;
  source?: string;
  mission_id?: string;
  workspace_id?: string;
  pending_question?: { target: QuestionIdentity; prompt: string };
  progression?: ExecutionGate;
  available: boolean;
  message?: string;
  delivery?: string;
  header?: Record<string, unknown>;
  attention?: { kind?: string; text?: string; task?: string; hint?: string };
  progress?: { criterion?: number; criterion_total?: number; criterion_title?: string };
  detail?: {
    criterion?: number;
    criterion_total?: number;
    criterion_state?: string;
    criterion_title?: string;
    last?: string;
    log_path?: string;
    worktree?: string;
  };
  health?: { last_error?: string };
  log_title?: string;
  log_lines?: string[];
  plans?: { name: string; status?: string; done?: boolean }[];
  waves?: { rows?: { task?: string; title?: string; state?: string; gates?: string[]; commit?: string }[] }[];
};

export const token = (() => {
  const fromURL = new URLSearchParams(location.search).get("token") || "";
  const value = fromURL || localStorage.getItem("bt-token") || "";
  if (value) {
    localStorage.setItem("bt-token", value);
    if (new URLSearchParams(location.search).has("token")) {
      const next = new URL(location.href);
      next.searchParams.delete("token");
      history.replaceState(null, "", next.pathname + next.search + next.hash);
    }
  }
  return value;
})();

export function webClientID() {
  const key = "bt-client";
  try {
    const existing = sessionStorage.getItem(key);
    if (existing) return existing;
    const id = "web-" + (crypto.randomUUID ? crypto.randomUUID() : String(Date.now()));
    sessionStorage.setItem(key, id);
    return id;
  } catch {
    return "web-" + String(Date.now());
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    redirect: ["GET", "HEAD"].includes((init?.method || "GET").toUpperCase()) ? init?.redirect : "error",
    headers: {
      Authorization: `Bearer ${localStorage.getItem("bt-token") || ""}`,
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(init?.headers || {}),
    },
  });
  init?.signal?.throwIfAborted();
  if (!res.ok) {
    if (res.status === 401) {
      try {
        localStorage.removeItem("bt-token");
      } catch {
        /* ignore */
      }
      throw new Error(T("err_token"));
    }
    throw new Error(await res.text());
  }
  const text = await res.text();
  init?.signal?.throwIfAborted();
  return (text ? JSON.parse(text) : null) as T;
}

export function gateMark(state?: string) {
  if (state === "pass") return "✓";
  if (state === "fail") return "✗";
  if (state === "silent") return "·";
  return "—";
}

export const gateStages = ["gate_finished", "gate_tree", "gate_tests", "gate_scope"] as const;

export function firstGates(watch: Watch | null): string[] {
  for (const wave of watch?.waves || []) {
    for (const row of wave.rows || []) {
      const g = row.gates || [];
      return [0, 1, 2, 3].map((i) => gateMark(g[i]));
    }
  }
  return ["—", "—", "—", "—"];
}

export function attentionLine(watch: Watch | null) {
  const kind = watch?.attention?.kind === "none" ? "" : watch?.attention?.kind || "";
  const text = watch?.attention?.text || "";
  if (!kind && !text) return "";
  return [kind, text].filter(Boolean).join(" · ");
}

export function processLife(process?: string) {
  if (process === "running") return T("alive");
  if (process === "unknown") return T("catalog_dead");
  if (process === "exited") return T("exited");
  return process || "—";
}

export function relTime(when?: string) {
  if (!when) return "—";
  const ms = Date.now() - new Date(when).getTime();
  if (Number.isNaN(ms)) return "—";
  const s = Math.max(0, Math.round(ms / 1000));
  const rtf = new Intl.RelativeTimeFormat(localeTag(), { numeric: "always" });
  if (s < 60) return rtf.format(-s, "second");
  const m = Math.round(s / 60);
  if (m < 60) return rtf.format(-m, "minute");
  return rtf.format(-Math.round(m / 60), "hour");
}

export function limitLabel(lim?: Limit) {
  if (!lim || lim.percent == null) return T("unknown");
  return `${Math.round(lim.percent)}%`;
}

export function taskRows(watch: Watch | null) {
  const out: { task: string; title: string; state: string; gates: string[]; commit: string }[] = [];
  for (const wave of watch?.waves || []) {
    for (const row of wave.rows || []) {
      out.push({
        task: row.task || "",
        title: row.title || row.task || "",
        state: row.state || "",
        gates: [0, 1, 2, 3].map((i) => row.gates?.[i] || "pending"),
        commit: row.commit || "",
      });
    }
  }
  return out;
}

export function criteria(watch: Watch | null) {
  const total = watch?.progress?.criterion_total || watch?.detail?.criterion_total || 0;
  const cur = watch?.progress?.criterion || watch?.detail?.criterion || 0;
  const title = watch?.progress?.criterion_title || watch?.detail?.criterion_title || "";
  const state = (watch?.detail?.criterion_state || "").toUpperCase();
  if (!total && !cur && !title) return [];
  const n = total > 0 ? total : Math.max(cur, 1);
  return Array.from({ length: n }, (_, idx) => {
    const i = idx + 1;
    let st = "pending";
    if (cur > 0 && i < cur) st = "pass";
    if (i === cur) {
      if (state === "DONE") st = "pass";
      else if (state === "FAIL" || state === "FAILED") st = "fail";
    }
    return { index: i, title: i === cur && title ? title : T("criterion_n", { n: i }), state: st };
  });
}

export function evidence(watch: Watch | null) {
  const out: { kind: string; text: string; origin: string }[] = [];
  if (watch?.detail?.last) out.push({ kind: "journal", text: watch.detail.last, origin: "watch" });
  if (watch?.detail?.log_path) out.push({ kind: "log", text: watch.detail.log_path, origin: "journal" });
  if (watch?.detail?.worktree) out.push({ kind: "tree", text: watch.detail.worktree, origin: "journal" });
  if (watch?.health?.last_error) out.push({ kind: "error", text: watch.health.last_error, origin: "health" });
  for (const line of watch?.log_lines || []) {
    if (line) out.push({ kind: "log", text: line, origin: "journal" });
  }
  return out;
}

export function greeting() {
  const h = new Date().getHours();
  if (h < 12) return T("greet_morning");
  if (h < 18) return T("greet_afternoon");
  return T("greet_evening");
}

export function initials(name: string) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "BT";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[1][0]).toUpperCase();
}

export function missionTone(status: string, attention?: string): "accent" | "warning" | "success" | "neutral" {
  if (attention) return "warning";
  const s = status.toLowerCase();
  if (s.includes("done") || s.includes("complete") || s.includes("conclu")) return "success";
  if (s.includes("wait") || s.includes("block") || s.includes("paus")) return "warning";
  if (s.includes("run") || s.includes("active") || s.includes("exec") || s.includes("progress")) return "accent";
  return "neutral";
}

export function missionStatusLabel(status: string, attention?: string) {
  if (attention) return T("status_waiting");
  const s = status.toLowerCase();
  if (s.includes("done") || s.includes("complete") || s.includes("conclu")) return T("status_done");
  if (s.includes("wait") || s.includes("block") || s.includes("paus")) return T("status_waiting");
  if (s.includes("run") || s.includes("active") || s.includes("exec") || s.includes("progress")) return T("status_running");
  if (!s || s === "open") return T("status_open");
  return status || "—";
}

export function plural(n: number, one: Msg, many: Msg) {
  return T(n === 1 ? one : many, { n });
}

export function isolationLabel(iso: string) {
  if (iso === "supported") return { text: T("iso_supported"), tone: "success" as const };
  if (iso === "not_yet_qualified") return { text: T("iso_not_qualified"), tone: "warning" as const };
  if (iso === "unsupported") return { text: T("iso_unsupported"), tone: "danger" as const };
  return { text: iso || "—", tone: "neutral" as const };
}

export function eventOrigin(kind: string): { label: string; tone: "warning" | "accent" | "success" | "neutral" } {
  if (kind.startsWith("watch") || kind.includes("delivery")) return { label: T("origin_watch"), tone: "warning" };
  if (kind.startsWith("account") || kind.includes("limit")) return { label: T("origin_limits"), tone: "neutral" };
  if (kind.startsWith("session")) return { label: T("origin_daemon"), tone: "accent" };
  if (kind.includes("inject") || kind.includes("batuta")) return { label: T("origin_integration"), tone: "success" };
  return { label: T("origin_daemon"), tone: "accent" };
}

export function slugify(name: string) {
  return name
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 40);
}

export function clock(at: string) {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return d.toISOString().slice(11, 19);
}

export type MissionBinding = {
  workspace_id: string; workspace_root: string; canonical_root: string;
  plan_path: string; plan_slug: string; plan_digest: string;
  delivery_id?: string; opened_digest?: string;
};
export type PlanIdentity = { mission_id: string; binding: MissionBinding; version: string };
export type PlanTask = {
  id: string; number: number; title: string; status: string; domain: string; complexity: string;
  dependencies: string[] | null; executor: string; model: string; scope: string[] | null; accept: string[] | null; line: number;
};
export type PlanApproval = { id: string; identity: PlanIdentity; content: string; approved_at: string };
export type PlanDetail = {
  identity: PlanIdentity; content: string; title: string; goal: string; context: string;
  created: string; inputs: string; status: string; tasks: PlanTask[]; approval?: PlanApproval;
};
export type QuestionIdentity = {
  mission_id: string; workspace_id: string; delivery: string; task: string;
  execution: number; question_id: string; opened_digest: string;
};
export type ExecutionGate = { required: boolean; cleared: boolean; delivery: string; review_id?: string; evidence_digest?: string; reason?: string };
export type ExecutionState = {
  run_id?: string;
  target: PlanIdentity; approval_id: string; delivery: string; opened_digest?: string;
  active: boolean; bound: boolean; watch?: Watch; gate?: ExecutionGate;
};
export type JourneyFailure = { code: string; message: string };
export type ApprovalResult = { target: PlanIdentity; effect: "recorded" | "rejected" | "uncertain"; receipt?: PlanApproval; error?: JourneyFailure };
export type ExecutionResult = {
  run_id?: string;
  target: PlanIdentity; approval_id: string; effect: "started" | "rejected" | "uncertain";
  delivery?: string; opened_digest?: string; error?: JourneyFailure;
};
export type AnswerResult = { target: QuestionIdentity; delivery?: string; effect: "recorded" | "rejected" | "uncertain"; error?: JourneyFailure };

// Mutation errors carry a typed effect even on non-2xx HTTP responses.
// Missing/malformed replies are uncertain; callers must inspect before retrying.
export async function journeyMutation<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, { signal, method: "POST", redirect: "error", headers: {
    Authorization: `Bearer ${localStorage.getItem("bt-token") || ""}`, "Content-Type": "application/json",
  }, body: JSON.stringify(body) });
  signal?.throwIfAborted();
  if (response.status === 401) {
    localStorage.removeItem("bt-token");
    throw new Error(T("err_token"));
  }
  return response.json();
}

export function sameBinding(a: MissionBinding, b: MissionBinding, opening = true): boolean {
  return !!a && !!b && a.workspace_id === b.workspace_id && a.workspace_root === b.workspace_root &&
    a.canonical_root === b.canonical_root && a.plan_path === b.plan_path && a.plan_slug === b.plan_slug &&
    a.plan_digest === b.plan_digest && (!opening || ((a.delivery_id || "") === (b.delivery_id || "") && (a.opened_digest || "") === (b.opened_digest || "")));
}

export function samePlan(a: PlanIdentity, b: PlanIdentity): boolean {
  return !!a && !!b && a.mission_id === b.mission_id && a.version === b.version && sameBinding(a.binding, b.binding);
}

export function sameQuestion(a: QuestionIdentity, b: QuestionIdentity): boolean {
  return !!a && !!b && a.mission_id === b.mission_id && a.workspace_id === b.workspace_id && a.delivery === b.delivery &&
    a.task === b.task && a.execution === b.execution && a.question_id === b.question_id && a.opened_digest === b.opened_digest;
}

export async function verifiedPlanText(identity: PlanIdentity, content: string, mission: Mission): Promise<string> {
  const b = identity?.binding;
  if (!b || identity.mission_id !== mission.id || b.workspace_id !== mission.workspace_id ||
      ![b.workspace_root, b.canonical_root, b.plan_path, b.plan_slug, b.plan_digest].every(v => typeof v === "string" && v.length > 0) ||
      !/^[a-f0-9]{64}$/.test(identity.version) || Boolean(b.delivery_id) !== Boolean(b.opened_digest) ||
      (mission.binding && !sameBinding(b, mission.binding, false)) || typeof content !== "string" || !content.length) {
    throw new Error(T("journey_invalid_plan"));
  }
  try {
    const raw = atob(content);
    if (btoa(raw) !== content || raw.length > 1048576) throw new Error("base64");
    const bytes = Uint8Array.from(raw, c => c.charCodeAt(0));
    const hash = await crypto.subtle.digest("SHA-256", bytes);
    const version = Array.from(new Uint8Array(hash), n => n.toString(16).padStart(2, "0")).join("");
    if (version !== identity.version) throw new Error("digest");
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw new Error(T("journey_invalid_plan"));
  }
}

export async function verifyReceipt(receipt: PlanApproval, identity: PlanIdentity, content: string, mission: Mission): Promise<void> {
  if (!receipt || typeof receipt.id !== "string" || !receipt.id || !samePlan(receipt.identity, identity) || receipt.content !== content || !!receipt.identity.binding.delivery_id || !!receipt.identity.binding.opened_digest ||
      typeof receipt.approved_at !== "string" || !Number.isFinite(Date.parse(receipt.approved_at))) throw new Error(T("journey_invalid_plan"));
  await verifiedPlanText(receipt.identity, receipt.content, mission);
}

export function verifyExecution(state: ExecutionState, receipt: PlanApproval, previous?: ExecutionState | null): void {
  if (!state || !samePlan(state.target, receipt.identity) || state.approval_id !== receipt.id ||
      typeof state.delivery !== "string" || !state.delivery || typeof state.active !== "boolean" || typeof state.bound !== "boolean" ||
      (state.active ? !validRunID(state.run_id) : state.run_id !== undefined && state.run_id !== "") ||
      (previous && (previous.delivery !== state.delivery || (previous.opened_digest && previous.opened_digest !== state.opened_digest)))) {
    throw new Error(T("journey_invalid_state"));
  }
  if (!state.bound && !state.opened_digest && !state.watch) return;
  const watch = state.watch;
  const gate = state.gate;
  const progression = watch?.progression;
  if (!state.opened_digest || !watch?.available || watch.source !== "current" || watch.scope !== "mission" ||
      watch.mission_id !== receipt.identity.mission_id || watch.workspace_id !== receipt.identity.binding.workspace_id || watch.delivery !== state.delivery ||
      typeof watch.header?.state !== "string" || !gate || typeof gate.required !== "boolean" || typeof gate.cleared !== "boolean" || gate.delivery !== state.delivery ||
      !progression || progression.delivery !== state.delivery || progression.required !== gate.required || progression.cleared !== gate.cleared) {
    throw new Error(T("journey_invalid_state"));
  }
  const q = watch.pending_question?.target;
  if (watch.pending_question && (!q || q.mission_id !== receipt.identity.mission_id || q.workspace_id !== receipt.identity.binding.workspace_id ||
      q.delivery !== state.delivery || q.opened_digest !== state.opened_digest || typeof q.task !== "string" || !q.task || typeof q.question_id !== "string" || !q.question_id || !Number.isSafeInteger(q.execution) || q.execution < 1 ||
      typeof watch.pending_question.prompt !== "string" || !watch.pending_question.prompt)) throw new Error(T("journey_invalid_state"));
}

export function executionLabel(state: ExecutionState | null, approved: boolean, unavailable: boolean): string {
  if (unavailable) return T("journey_unavailable");
  if (!state?.opened_digest) return state?.active ? T("status_running") : approved ? T("journey_approved") : T("journey_proposed");
  const backend = state.watch?.header?.state;
  if (state.watch?.pending_question) return T("status_waiting");
  if (state.active) return T("status_running");
  if (state.gate?.required && !state.gate.cleared) return T("journey_review_blocked");
  if (backend === "done") return T("journey_completed");
  if (backend === "review_blocked") return T("journey_review_blocked");
  if (backend === "running" || backend === "executing") return T("status_running");
  return T("status_waiting");
}

export type ExecutionStopRequest = { target: PlanIdentity; approval_id: string; delivery: string; run_id: string };
export type ExecutionStopResult = ExecutionStopRequest & {
  effect: "pending" | "completed" | "uncertain" | "rejected";
  finalized: boolean; durable: boolean; core_state?: string; state?: ExecutionState;
  causes?: string[]; error?: JourneyFailure;
};

export class ExecutionStopError extends Error {
  constructor(public result: ExecutionStopResult, public status: number, public cause?: unknown) {
    super(result.error ? `${result.error.code}: ${result.error.message}` : cause instanceof Error ? cause.message : T("journey_invalid_state"));
  }
}

export function validRunID(value: unknown): value is string {
  return typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
}

export async function executionDelivery(approval: string, target: PlanIdentity): Promise<string> {
  const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(`${approval}\0${target.mission_id}\0${target.binding.workspace_id}`));
  return "console-" + Array.from(new Uint8Array(hash), n => n.toString(16).padStart(2, "0")).join("");
}

function onlyKeys(value: unknown, keys: string[]): boolean {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).every(key => keys.includes(key));
}

function stopIdentityShape(target: PlanIdentity): boolean {
  return onlyKeys(target, ["mission_id", "binding", "version"]) && onlyKeys(target.binding, ["workspace_id", "workspace_root", "canonical_root", "plan_path", "plan_slug", "plan_digest", "delivery_id", "opened_digest"]);
}

function validStopProjection(state: ExecutionState, request: ExecutionStopRequest): boolean {
  if (!onlyKeys(state, ["target", "approval_id", "delivery", "run_id", "active", "bound", "opened_digest", "watch", "gate"]) || !stopIdentityShape(state.target) || !samePlan(state.target, request.target) || state.approval_id !== request.approval_id || state.delivery !== request.delivery || state.active !== false || (state.run_id !== undefined && state.run_id !== "") || typeof state.bound !== "boolean") return false;
  if (!state.opened_digest) return !state.watch && !state.bound && !state.gate;
  const watch = state.watch, gate = state.gate, progression = watch?.progression;
  const opening = (value: unknown) => typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
  if (!opening(state.opened_digest) || !watch || !gate || !progression || watch.source !== "current" || watch.delivery !== request.delivery ||
    watch.mission_id !== request.target.mission_id || watch.workspace_id !== request.target.binding.workspace_id || gate.required !== true || gate.delivery !== request.delivery ||
    typeof gate.cleared !== "boolean" || ["required", "cleared", "delivery", "review_id", "evidence_digest", "reason"].some(key => Reflect.get(gate, key) !== Reflect.get(progression, key)) ||
    (gate.evidence_digest && !opening(gate.evidence_digest)) || (gate.cleared && (!gate.review_id || !opening(gate.evidence_digest)))) return false;
  return watch.header?.state !== "done" || gate.cleared;
}

export async function stopExecution(request: ExecutionStopRequest, signal?: AbortSignal): Promise<ExecutionStopResult> {
  // Copy before the first await: callers cannot retarget an admitted request.
  const exact = structuredClone(request);
  let result: ExecutionStopResult = { ...exact, effect: "rejected", finalized: false, durable: false };
  let status = 0;
  try {
    signal?.throwIfAborted();
    const target = exact.target, b = target?.binding;
    if (!onlyKeys(exact, ["target", "approval_id", "delivery", "run_id"]) || !b || !stopIdentityShape(target) || ![target.mission_id, b.workspace_id, b.workspace_root, b.canonical_root, b.plan_path, b.plan_slug, b.plan_digest, exact.approval_id].every(v => typeof v === "string" && !!v) ||
      !validRunID(target.version) || b.delivery_id || b.opened_digest || !validRunID(exact.run_id) || exact.delivery !== await executionDelivery(exact.approval_id, target)) throw new Error(T("journey_invalid_state"));
    signal?.throwIfAborted();
    result = { ...result, effect: "uncertain" };
    const response = await fetch(`/v0/missions/${encodeURIComponent(target.mission_id)}/execution/stop`, {
      method: "POST", redirect: "error", signal, body: JSON.stringify(exact),
      headers: { Authorization: `Bearer ${localStorage.getItem("bt-token") || ""}`, "Content-Type": "application/json" },
    });
    status = response.status;
    signal?.throwIfAborted();
    if (status === 401 || status === 403) {
      result = { ...result, effect: "rejected" };
      if (status === 401) localStorage.removeItem("bt-token");
      throw new Error(T("err_token"));
    }
    const got: ExecutionStopResult = await response.json();
    signal?.throwIfAborted();
    let valid = onlyKeys(got, ["target", "approval_id", "delivery", "run_id", "effect", "finalized", "durable", "core_state", "state", "causes", "error"]) && stopIdentityShape(got.target) && samePlan(got.target, exact.target) && got.approval_id === exact.approval_id && got.delivery === exact.delivery && got.run_id === exact.run_id &&
      typeof got.finalized === "boolean" && typeof got.durable === "boolean" && (got.core_state === undefined || typeof got.core_state === "string") && (got.causes == null || (Array.isArray(got.causes) && got.causes.every(c => typeof c === "string"))) &&
      (got.state == null || onlyKeys(got.state, ["target", "approval_id", "delivery", "run_id", "active", "bound", "opened_digest", "watch", "gate"])) &&
      (got.error == null || (onlyKeys(got.error, ["code", "message"]) && typeof got.error.code === "string" && typeof got.error.message === "string"));
    const failure = got?.error && onlyKeys(got.error, ["code", "message"]) && typeof got.error.code === "string" && !!got.error.code && typeof got.error.message === "string";
    const empty = !got?.state && !got?.core_state && !got?.causes?.length;
    switch (got?.effect) {
      case "pending": valid = valid && status === 202 && !got.finalized && !got.durable && !got.error && empty; break;
      case "completed": valid = valid && status === 200 && got.finalized && got.durable && !got.error && !!got.state; break;
      case "uncertain": valid = valid && status === 503 && got.finalized && !!failure; break;
      case "rejected": valid = valid && status >= 400 && status < 600 && !got.finalized && !got.durable && !!failure && empty; break;
      default: valid = false;
    }
    if (got?.durable && !got.state) valid = false;
    if (got?.state) {
      valid = valid && validStopProjection(got.state, exact);
      if (got.durable) {
        valid = valid && (got.state.opened_digest ? !!got.state.bound && ["done", "blocked", "waiting_input", "waiting_plan", "canceled", "abandoned"].includes(got.core_state || "") : !got.core_state);
        if (got.effect === "completed" && got.state.opened_digest) valid = valid && got.state.watch?.header?.state === got.core_state;
      }
    }
    if (!valid) throw new Error(T("journey_invalid_state"));
    result = got;
    if (got.error) throw new ExecutionStopError(result, status);
    return result;
  } catch (cause) {
    if (cause instanceof ExecutionStopError) throw cause;
    throw new ExecutionStopError(result, status, cause);
  }
}
