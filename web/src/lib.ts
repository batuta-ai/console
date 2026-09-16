export type Screen = "home" | "missions" | "room" | "squads" | "editor" | "accounts" | "activity" | "review";

export type Workspace = { id: string; name: string; root: string };
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
  id: string;
  title: string;
  status: string;
  workspace_id: string;
  attention?: string;
  squad_snapshot?: { name?: string; members?: Member[] };
};
export type Account = {
  id: string;
  label: string;
  provider: string;
  adapter: string;
  isolation: string;
  connected: boolean;
  session_cap?: number;
  vault_ref?: string;
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
  id: string;
  process: string;
  owner: string;
  owner_client?: string;
  adapter: string;
  mission_id: string;
  account_id?: string;
  work?: string;
  batuta_injected?: boolean;
  skills_version?: string;
};
export type Watch = {
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
  log_lines?: string[];
  waves?: { rows?: { task?: string; title?: string; state?: string; gates?: string[] }[] }[];
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
    headers: {
      Authorization: `Bearer ${localStorage.getItem("bt-token") || ""}`,
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(init?.headers || {}),
    },
  });
  if (!res.ok) {
    if (res.status === 401) {
      try {
        localStorage.removeItem("bt-token");
      } catch {
        /* ignore */
      }
      throw new Error("token inválido. Rode console --web para abrir com o token deste home");
    }
    throw new Error(await res.text());
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : null) as T;
}

export function gateMark(state?: string) {
  if (state === "pass") return "✓";
  if (state === "fail") return "✗";
  return "—";
}

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
  if (process === "running") return "vivo";
  if (process === "unknown") return "só no catálogo — o processo não está vivo";
  if (process === "exited") return "encerrou";
  return process || "—";
}

export function relTime(when?: string) {
  if (!when) return "—";
  const ms = Date.now() - new Date(when).getTime();
  if (Number.isNaN(ms)) return "—";
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `há ${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `há ${m} min`;
  return `há ${Math.round(m / 60)} h`;
}

export function limitLabel(lim?: Limit) {
  if (!lim || lim.percent == null) return "desconhecido";
  return `${Math.round(lim.percent)}%`;
}

export function taskRows(watch: Watch | null) {
  const out: { task: string; title: string; state: string; gates: string[] }[] = [];
  for (const wave of watch?.waves || []) {
    for (const row of wave.rows || []) {
      out.push({
        task: row.task || "",
        title: row.title || row.task || "",
        state: row.state || "",
        gates: [0, 1, 2, 3].map((i) => gateMark(row.gates?.[i])),
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
    return { index: i, title: i === cur && title ? title : `critério ${i}`, state: st };
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
  if (h < 12) return "Bom dia";
  if (h < 18) return "Boa tarde";
  return "Boa noite";
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
  if (attention) return "Aguardando";
  const s = status.toLowerCase();
  if (s.includes("done") || s.includes("complete") || s.includes("conclu")) return "Concluída";
  if (s.includes("wait") || s.includes("block") || s.includes("paus")) return "Aguardando";
  if (s.includes("run") || s.includes("active") || s.includes("exec") || s.includes("progress")) return "Executando";
  if (!s || s === "open") return "Aberta";
  return status || "—";
}

export function plural(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`;
}

export function isolationLabel(iso: string) {
  if (iso === "supported") return { text: "suportado", tone: "success" as const };
  if (iso === "not_yet_qualified") return { text: "não qualificado", tone: "warning" as const };
  if (iso === "unsupported") return { text: "sem suporte", tone: "danger" as const };
  return { text: iso || "—", tone: "neutral" as const };
}

export function eventOrigin(kind: string): { label: string; tone: "warning" | "accent" | "success" | "neutral" } {
  if (kind.startsWith("watch") || kind.includes("delivery")) return { label: "watch", tone: "warning" };
  if (kind.startsWith("account") || kind.includes("limit")) return { label: "limites", tone: "neutral" };
  if (kind.startsWith("session")) return { label: "daemon", tone: "accent" };
  if (kind.includes("inject") || kind.includes("batuta")) return { label: "integração", tone: "success" };
  return { label: "daemon", tone: "accent" };
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
