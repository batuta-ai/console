import { useEffect, useMemo, useRef, useState } from "react";
import { Activity, House, KeyRound, Moon, Search, Sun, Target, Users } from "lucide-react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  api,
  attentionLine,
  clock,
  criteria,
  evidence,
  eventOrigin,
  firstGates,
  greeting,
  isolationLabel,
  limitLabel,
  missionStatusLabel,
  missionTone,
  processLife,
  relTime,
  slugify,
  taskRows,
  webClientID,
  type Account,
  type Event,
  type Limit,
  type Member,
  type Mission,
  type Screen,
  type Session,
  type Skill,
  type Squad,
  type Watch,
  type Workspace,
  plural,
} from "./lib";
import { Alert, Avatar, Badge, Button, Dialog, Drawer, Field, IconButton, Input, Progress, Select, Stat, Stepper, Tabs, WorkspaceSwitcher } from "./ui";

const webClient = webClientID();

const NAV: { id: Screen; label: string; icon: typeof House }[] = [
  { id: "home", label: "Início", icon: House },
  { id: "missions", label: "Missões", icon: Target },
  { id: "squads", label: "Squads", icon: Users },
  { id: "accounts", label: "Contas e limites", icon: KeyRound },
  { id: "activity", label: "Atividade", icon: Activity },
];

function useMQ(query: string) {
  const [ok, setOk] = useState(() => (typeof window !== "undefined" ? window.matchMedia(query).matches : false));
  useEffect(() => {
    const mm = window.matchMedia(query);
    const on = () => setOk(mm.matches);
    on();
    mm.addEventListener("change", on);
    return () => mm.removeEventListener("change", on);
  }, [query]);
  return ok;
}

function screenTitle(screen: Screen, mission: Mission | null, squad: Squad | null) {
  if (screen === "home") return "Início";
  if (screen === "missions") return "Missões";
  if (screen === "room") return mission?.title || "Sala da missão";
  if (screen === "squads") return "Squads";
  if (screen === "editor") return squad?.name || "Editor de squad";
  if (screen === "accounts") return "Contas e limites";
  if (screen === "activity") return "Atividade";
  return "Revisão da entrega";
}

export function App() {
  const narrow = useMQ("(max-width: 999px)");
  const [theme, setTheme] = useState(() => localStorage.getItem("bt-theme") || "light");
  const [screen, setScreen] = useState<Screen>("home");
  const [err, setErr] = useState("");
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [missions, setMissions] = useState<Mission[]>([]);
  const [squads, setSquads] = useState<Squad[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [activity, setActivity] = useState<Event[]>([]);
  const [limits, setLimits] = useState<Limit[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [watch, setWatch] = useState<Watch | null>(null);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [skillsRoot, setSkillsRoot] = useState("");
  const [editing, setEditing] = useState<Squad | null>(null);
  const [addAccount, setAddAccount] = useState(false);
  const [activeMission, setActiveMission] = useState<Mission | null>(null);
  const [wsId, setWsId] = useState(() => localStorage.getItem("bt-ws") || "");
  const [wsDialog, setWsDialog] = useState(false);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("bt-theme", theme);
  }, [theme]);

  const currentWs = workspaces.find((w) => w.id === wsId) || workspaces[0];
  useEffect(() => {
    if (currentWs && currentWs.id !== wsId) {
      setWsId(currentWs.id);
      localStorage.setItem("bt-ws", currentWs.id);
    }
  }, [currentWs, wsId]);

  async function refresh() {
    try {
      const [ws, msn, sq, ac, ev, sess, lim, sk] = await Promise.all([
        api<Workspace[]>("/v0/workspaces"),
        api<Mission[]>("/v0/missions"),
        api<Squad[]>("/v0/squads"),
        api<Account[]>("/v0/accounts"),
        api<Event[]>("/v0/activity"),
        api<Session[]>("/v0/sessions"),
        api<Limit[]>("/v0/limits"),
        api<{ root?: string; skills?: Skill[] }>("/v0/skills"),
      ]);
      setWorkspaces(ws || []);
      setMissions(msn || []);
      setSquads(sq || []);
      setAccounts(ac || []);
      setActivity(ev || []);
      setSessions(sess || []);
      setLimits(lim || []);
      setSkills(sk?.skills || []);
      setSkillsRoot(sk?.root || "");
      setErr("");
      const watchID = activeMission?.workspace_id || (ws || []).find((w) => w.id === wsId)?.id || ws?.[0]?.id;
      if (watchID) {
        const w = await api<Watch>(`/v0/watch?workspace=${encodeURIComponent(watchID)}`);
        setWatch(w);
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    refreshRef.current();
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const tok = localStorage.getItem("bt-token") || "";
    const sock = new WebSocket(`${proto}://${location.host}/v0/events?token=${encodeURIComponent(tok)}`);
    let timer = 0;
    sock.onmessage = () => {
      if (timer) return;
      timer = window.setTimeout(() => {
        timer = 0;
        refreshRef.current();
      }, 200);
    };
    return () => {
      sock.close();
      if (timer) window.clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    const id = activeMission?.workspace_id || currentWs?.id;
    if (!id) return;
    api<Watch>(`/v0/watch?workspace=${encodeURIComponent(id)}`)
      .then(setWatch)
      .catch(() => {});
  }, [activeMission?.workspace_id, currentWs?.id]);

  const attention = useMemo(() => attentionLine(watch), [watch]);
  const wsMissions = missions.filter((m) => !currentWs || m.workspace_id === currentWs.id);
  const attentionCount = attention ? 1 : 0;

  function go(id: Screen) {
    setScreen(id);
    setAddAccount(false);
  }

  function pickWorkspace(id: string) {
    setWsId(id);
    localStorage.setItem("bt-ws", id);
    go("missions");
  }

  const liveClients = new Set(sessions.filter((s) => s.process === "running").map((s) => s.owner_client || s.owner)).size;

  return (
    <div className="frame" data-theme={theme} data-density="comfortable">
      <div className={narrow ? "web web--rail" : "web"}>
        <aside className="sidebar">
          <div className="brand">
            <img src={theme === "dark" ? "/assets/batuta-symbol-white.svg" : "/assets/batuta-symbol.svg"} alt="" />
            <span>
              batuta <span className="brand-muted">/</span> console
            </span>
          </div>
          <div className="side-pad">
            <WorkspaceSwitcher
              workspaces={workspaces}
              current={currentWs}
              collapsed={narrow}
              onChange={pickWorkspace}
              onCreate={() => setWsDialog(true)}
            />
          </div>
          <nav className="side-nav">
            {NAV.map((item) => {
              const Icon = item.icon;
              const active = screen === item.id || (item.id === "squads" && screen === "editor");
              return (
                <button key={item.id} type="button" className={active ? "nav-btn nav-btn--active" : "nav-btn"} onClick={() => go(item.id)}>
                  <Icon size={17} strokeWidth={2} />
                  <span className="label" style={{ flex: 1 }}>
                    {item.label}
                  </span>
                  {item.id === "missions" && attentionCount && screen !== "home" ? <span className="nav-count">{attentionCount}</span> : null}
                </button>
              );
            })}
          </nav>
          <div className="side-foot">
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <span className="live-dot" />
              <span className="foot-copy">consoled · ativo</span>
            </div>
            <span className="foot-copy">
              API v0 · {sessions.filter((s) => s.process === "running").length} sessões · {liveClients} clientes
            </span>
          </div>
        </aside>
        <div className="shell">
          <header className="topbar">
            <div className="crumb">
              <span>{currentWs?.name || "workspace"}</span>
              <span>/</span>
              <span className="crumb-now">{screenTitle(screen, activeMission, editing)}</span>
            </div>
            {attention ? (
              <button type="button" className="need-you" onClick={() => go("room")}>
                <span className="live-dot bt-pulse" style={{ background: "var(--warning)" }} />
                1 precisa de você
              </button>
            ) : null}
            <IconButton label="Buscar" title="Busca ainda não está no daemon">
              <Search size={17} />
            </IconButton>
            <IconButton label="Alternar tema" onClick={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}>
              {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
            </IconButton>
            <Avatar name="Batuta" size="sm" />
          </header>
          <div className="content">
            {err ? (
              <div className="err">
                {err} — passe ?token= a partir de <code>console --web</code>
              </div>
            ) : null}
            {screen === "home" && (
              <Home
                workspaces={workspaces}
                missions={wsMissions}
                accounts={accounts}
                limits={limits}
                sessions={sessions}
                attention={attention}
                wsName={currentWs?.name || "—"}
                onOpenRoom={(m) => {
                  setActiveMission(m);
                  go("room");
                }}
                onPickWorkspace={pickWorkspace}
                onNewWorkspace={() => setWsDialog(true)}
                onMissions={() => go("missions")}
                onAccounts={() => go("accounts")}
                onRoom={() => go("room")}
              />
            )}
            {screen === "missions" && (
              <Missions
                missions={wsMissions}
                squads={squads}
                workspace={currentWs}
                watch={watch}
                attention={attention}
                onOpen={(m) => {
                  setActiveMission(m);
                  go("room");
                }}
                onCreated={refresh}
              />
            )}
            {screen === "room" && (
              <Room
                mission={activeMission}
                sessions={sessions.filter((s) => !activeMission || s.mission_id === activeMission.id)}
                watch={watch}
                accounts={accounts}
                activity={activity}
                client={webClient}
                skillsRoot={skillsRoot}
                onRefresh={refresh}
                onReview={() => setScreen("review")}
              />
            )}
            {screen === "squads" && (
              <Squads
                squads={squads}
                skills={skills}
                skillsRoot={skillsRoot}
                onCreate={async () => {
                  const q = await api<Squad>("/v0/squads", { method: "POST", body: JSON.stringify({ name: "Novo squad", purpose: "", origin: "custom", members: [] }) });
                  setEditing(q);
                  setScreen("editor");
                  refresh();
                }}
                onEdit={(q) => {
                  setEditing(q);
                  setScreen("editor");
                }}
                onDuplicate={async (q) => {
                  await api("/v0/squads", {
                    method: "POST",
                    body: JSON.stringify({ name: `${q.name} (cópia)`, purpose: q.purpose || "", origin: "custom", members: q.members || [] }),
                  });
                  refresh();
                }}
              />
            )}
            {screen === "editor" && editing && (
              <SquadEditor
                squad={editing}
                accounts={accounts}
                skills={skills}
                workspaceId={currentWs?.id}
                onClose={() => go("squads")}
                onSaved={() => {
                  go("squads");
                  refresh();
                }}
              />
            )}
            {screen === "accounts" && (
              <Accounts
                accounts={accounts}
                limits={limits}
                sessions={sessions}
                open={addAccount}
                onOpen={() => setAddAccount(true)}
                onClose={() => setAddAccount(false)}
                onSaved={() => {
                  setAddAccount(false);
                  refresh();
                }}
              />
            )}
            {screen === "activity" && <ActivityScreen activity={activity} sessions={sessions} missions={missions} onOpenMission={(m) => { setActiveMission(m); go("room"); }} />}
            {screen === "review" && <Review watch={watch} mission={activeMission} onBack={() => go("room")} />}
          </div>
        </div>
      </div>
      <CreateWorkspaceDialog
        open={wsDialog}
        onClose={() => setWsDialog(false)}
        onCreated={(w) => {
          setWsDialog(false);
          pickWorkspace(w.id);
          refresh();
        }}
      />
    </div>
  );
}

function Home({
  workspaces,
  missions,
  accounts,
  limits,
  sessions,
  attention,
  wsName,
  onOpenRoom,
  onPickWorkspace,
  onNewWorkspace,
  onMissions,
  onAccounts,
  onRoom,
}: {
  workspaces: Workspace[];
  missions: Mission[];
  accounts: Account[];
  limits: Limit[];
  sessions: Session[];
  attention: string;
  wsName: string;
  onOpenRoom: (m: Mission) => void;
  onPickWorkspace: (id: string) => void;
  onNewWorkspace: () => void;
  onMissions: () => void;
  onAccounts: () => void;
  onRoom: () => void;
}) {
  const active = missions.filter((m) => !/done|complete|conclu/i.test(m.status));
  return (
    <div className="page" data-screen-label="Início">
      <div className="page-head">
        <div>
          <h1>{greeting()}</h1>
          <p className="sub">
            {active.length} entrega{active.length === 1 ? "" : "s"} ativa{active.length === 1 ? "" : "s"} em <span className="mono">{wsName}</span>
            {attention ? " · 1 item pede sua atenção." : "."}
          </p>
        </div>
        <Button variant="secondary" onClick={onNewWorkspace}>
          Novo workspace
        </Button>
      </div>
      {attention ? (
        <div className="banner">
          <span className="banner-dot bt-pulse" />
          <div style={{ flex: 1, minWidth: 0 }}>
            <strong style={{ color: "var(--warning-text)" }}>Precisa de você</strong> <span>· {attention}</span>
          </div>
          <Button size="sm" onClick={onRoom}>
            Abrir sala
          </Button>
        </div>
      ) : null}
      <div className="cards">
        {workspaces.map((w) => {
          const n = missions.filter((m) => m.workspace_id === w.id && !/done|complete|conclu/i.test(m.status)).length;
          return (
            <button key={w.id} type="button" className="ws-card" onClick={() => onPickWorkspace(w.id)}>
              <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
                <span className="mono-name">{w.name}</span>
                <Badge tone={n ? "accent" : "neutral"} dot>
                  {n ? `${n} ativas` : "ociosa"}
                </Badge>
              </div>
              <div className="path">{w.root}</div>
              <div style={{ fontSize: 13, color: "var(--text-secondary)" }}>
                {n ? `${plural(n, "missão em andamento", "missões em andamento")}` : "Nenhuma missão ativa neste workspace."}
              </div>
            </button>
          );
        })}
        {workspaces.length === 0 ? <div className="panel empty">Nenhum workspace ainda. Crie o primeiro para operar uma CLI.</div> : null}
      </div>
      <div className="cards-wide">
        <section className="panel">
          <div className="panel-h">
            <span>Entregas ativas</span>
            <button type="button" className="linkish" onClick={onMissions}>
              Ver missões
            </button>
          </div>
          {active.length === 0 ? <div className="empty">Nenhuma entrega ativa neste workspace.</div> : null}
          {active.map((m) => (
            <button key={m.id} type="button" className="panel-row" onClick={() => onOpenRoom(m)}>
              <span style={{ fontWeight: 500 }}>{m.title}</span>
              <span className="mono muted" style={{ fontSize: 12 }}>
                {m.id}
              </span>
              <span style={{ fontSize: 12.5, color: "var(--text-secondary)" }}>
                {m.squad_snapshot?.name || "—"} · {m.attention || "acompanhar gates"}
              </span>
              <span style={{ justifySelf: "end" }}>
                <Badge tone={missionTone(m.status, m.attention)} dot>
                  {missionStatusLabel(m.status, m.attention)}
                </Badge>
              </span>
            </button>
          ))}
        </section>
        <section className="panel">
          <div className="panel-h">Contas e limites</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 12, padding: "var(--cardpad)" }}>
            {accounts.slice(0, 3).map((a) => {
              const lim = limits.find((l) => l.account_id === a.id);
              return (
                <div key={a.id} style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                  <div style={{ display: "flex", justifyContent: "space-between", fontSize: 13 }}>
                    <span className="mono">{a.label}</span>
                    <span className="muted">{limitLabel(lim)}</span>
                  </div>
                  <Progress value={lim?.percent} unknown={lim?.percent == null} />
                </div>
              );
            })}
            {accounts.length === 0 ? <span className="muted">Nenhuma conta cadastrada.</span> : null}
            <button type="button" className="linkish" onClick={onAccounts}>
              Ver todas as contas
            </button>
            <span className="muted" style={{ fontSize: 12 }}>
              {sessions.filter((s) => s.process === "running").length} sessões vivas no daemon.
            </span>
          </div>
        </section>
      </div>
    </div>
  );
}

function Missions({
  missions,
  squads,
  workspace,
  watch,
  attention,
  onOpen,
  onCreated,
}: {
  missions: Mission[];
  squads: Squad[];
  workspace?: Workspace;
  watch: Watch | null;
  attention: string;
  onOpen: (m: Mission) => void;
  onCreated: () => void;
}) {
  const [filter, setFilter] = useState("all");
  const active = missions.filter((m) => !/done|complete|conclu/i.test(m.status));
  const done = missions.filter((m) => /done|complete|conclu/i.test(m.status));
  const shown = filter === "active" ? active : filter === "done" ? done : missions;
  const crit = criteria(watch);
  const pct = crit.length ? Math.round((100 * crit.filter((c) => c.state === "pass").length) / crit.length) : null;
  return (
    <div className="page">
      <div className="page-head">
        <h1>Missões</h1>
        <Button
          onClick={async () => {
            if (!workspace || !squads[0]) return alert("Crie um workspace e um squad primeiro");
            await api("/v0/missions", { method: "POST", body: JSON.stringify({ workspace_id: workspace.id, title: "Nova missão", squad_id: squads[0].id }) });
            onCreated();
          }}
        >
          Nova missão
        </Button>
      </div>
      <Tabs
        active={filter}
        onChange={setFilter}
        items={[
          { id: "all", label: "Todas", count: missions.length },
          { id: "active", label: "Ativas", count: active.length },
          { id: "done", label: "Concluídas", count: done.length },
        ]}
      />
      <section className="panel">
        <div className="mission-grid caps" style={{ cursor: "default" }}>
          <span>Objetivo</span>
          <span>Squad</span>
          <span>Situação</span>
          <span>Andamento verificado</span>
          <span>Próxima ação</span>
        </div>
        {shown.length === 0 ? <div className="empty">Nenhuma missão neste filtro.</div> : null}
        {shown.map((m) => {
          const attn = Boolean(m.attention) || (attention && activeMissionMatch(m, watch));
          return (
            <button key={m.id} type="button" className={attn ? "mission-grid row-warn" : "mission-grid"} onClick={() => onOpen(m)}>
              <span style={{ display: "flex", flexDirection: "column" }}>
                <span style={{ fontWeight: 500 }}>{m.title}</span>
                <span className="mono muted" style={{ fontSize: 12 }}>
                  {m.id}
                </span>
              </span>
              <span style={{ fontSize: 13 }}>{m.squad_snapshot?.name || "—"}</span>
              <span>
                <Badge tone={missionTone(m.status, attn ? "1" : m.attention)} dot>
                  {missionStatusLabel(m.status, attn ? "1" : m.attention)}
                </Badge>
              </span>
              <span style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                <span className="mono" style={{ fontSize: 12 }}>
                  {pct == null ? "sem journal" : `${pct}% critérios`}
                </span>
                <Progress value={pct} unknown={pct == null} />
              </span>
              <span style={{ fontSize: 13, color: attn ? "var(--warning-text)" : undefined, fontWeight: attn ? 600 : 400 }}>
                {attn ? attention || m.attention : "acompanhar gates"}
              </span>
            </button>
          );
        })}
      </section>
    </div>
  );
}

function activeMissionMatch(m: Mission, watch: Watch | null) {
  return Boolean(watch?.available && m.title && watch.delivery);
}

function Room({
  mission,
  sessions,
  watch,
  accounts,
  activity,
  client,
  skillsRoot,
  onRefresh,
  onReview,
}: {
  mission: Mission | null;
  sessions: Session[];
  watch: Watch | null;
  accounts: Account[];
  activity: Event[];
  client: string;
  skillsRoot: string;
  onRefresh: () => void;
  onReview: () => void;
}) {
  const [layout, setLayout] = useState<"list" | "grid" | "terminal">("list");
  const [logTab, setLogTab] = useState("log");
  const [sel, setSel] = useState(0);
  const [answer, setAnswer] = useState("");
  const [inject, setInject] = useState(true);
  const live = sessions.find((s) => s.process === "running");
  const shown = live || sessions[0];
  const owns = live?.owner === "web" && live.owner_client === client;
  const members = mission?.squad_snapshot?.members || [];
  const gates = firstGates(watch);
  const attn = attentionLine(watch);
  const rows = taskRows(watch);
  const tasks = rows.length
    ? rows.map((r, i) => ({
        key: r.task || String(i),
        name: r.title,
        role: members[i]?.role || "agente",
        state: r.state,
        account: accounts.find((a) => a.id === members[i]?.account_id)?.label || members[i]?.account_id || "—",
        cli: members[i]?.cli || "",
        model: members[i]?.model || "herdar",
        task: r.task,
        gates: r.gates,
      }))
    : members.map((mb, i) => ({
        key: mb.id || String(i),
        name: mb.role || "membro",
        role: mb.role || "agente",
        state: "",
        account: accounts.find((a) => a.id === mb.account_id)?.label || mb.account_id || "—",
        cli: mb.cli,
        model: mb.model || "herdar",
        task: "",
        gates: ["—", "—", "—", "—"],
      }));
  const selected = tasks[sel] || tasks[0];
  const crit = criteria(watch);
  const ev = evidence(watch);
  const integrated = rows.filter((r) => /done|pass|integr/i.test(r.state)).length;

  function taskSym(state: string) {
    if (/done|pass|integr/i.test(state)) return { s: "✓", c: "var(--success-text)" };
    if (/wait|block|ask|attention/i.test(state)) return { s: "?", c: "var(--warning-text)" };
    return { s: "›", c: "var(--accent)" };
  }

  return (
    <div className="page">
      {attn ? <div className="room-bar" /> : null}
      <div className="page-head">
        <div>
          <div className="inline-actions">
            <h1>{mission?.title || "Sala da missão"}</h1>
            {watch?.delivery ? <Badge tone="accent">{String(watch.delivery)}</Badge> : <Badge tone="neutral">sem journal Batuta</Badge>}
            {live?.batuta_injected ? <Badge tone="success">Batuta injetado</Badge> : live ? <Badge tone="neutral">CLI genérica</Badge> : null}
            {attn ? (
              <Badge tone="warning" dot>
                1 precisa de você
              </Badge>
            ) : null}
          </div>
          <p className="sub">
            <span className="mono">{mission?.id || "—"}</span> · {integrated}/{Math.max(tasks.length, 1)} tarefas no journal · {live ? "1 executando" : "nenhuma sessão viva"}
            {shown?.process === "unknown" ? " · processo só no catálogo" : ""}
          </p>
        </div>
        <div className="inline-actions">
          {["G0", "G1", "G2", "G3"].map((g, i) => (
            <span key={g} className={gates[i] === "✓" ? "gate gate--done" : "gate gate--wait"}>
              {g} {gates[i]}
            </span>
          ))}
          <Button variant="secondary" onClick={onReview}>
            Revisar entrega
          </Button>
          {mission && !live ? (
            <>
              <label className="inject-toggle">
                <input type="checkbox" checked={inject} onChange={(e) => setInject(e.target.checked)} />
                Injetar Batuta
              </label>
              <Button
                onClick={async () => {
                  const maestro = members.find((m) => m.maestro) || members[0];
                  await api("/v0/sessions", {
                    method: "POST",
                    body: JSON.stringify({
                      mission_id: mission.id,
                      member_id: maestro?.id,
                      owner: "web",
                      client,
                      batuta: inject,
                    }),
                  });
                  onRefresh();
                }}
              >
                Iniciar sessão
              </Button>
            </>
          ) : null}
        </div>
      </div>
      {inject && !skillsRoot && !live ? (
        <Alert tone="warning" title="Skills Batuta não encontradas">
          A sessão sobe mesmo assim, como CLI genérica. Instale as skills do Batuta ou defina BATUTA_SKILLS para injetar de verdade.
        </Alert>
      ) : null}
      {attn ? (
        <div className="banner">
          <span className="banner-dot bt-pulse" />
          <div style={{ flex: 1 }}>
            <strong style={{ color: "var(--warning-text)" }}>Precisa de você</strong> · {attn} Responder não dispara trabalho pago.
          </div>
          <Button
            size="sm"
            onClick={() => {
              const i = tasks.findIndex((t) => t.task && t.task === watch?.attention?.task);
              if (i >= 0) setSel(i);
              setLogTab("log");
            }}
          >
            Responder
          </Button>
        </div>
      ) : null}
      {shown?.process === "unknown" ? <Alert tone="warning" title="Histórico recuperado">{processLife("unknown")}</Alert> : null}
      <Tabs
        active={layout}
        onChange={(id) => setLayout(id as "list" | "grid" | "terminal")}
        items={[
          { id: "list", label: "Lista" },
          { id: "grid", label: "Grade" },
          { id: "terminal", label: "Terminal" },
        ]}
      />
      {layout === "list" && (
        <div className="cards-wide">
          <section className="panel">
            <div className="task-grid caps" style={{ cursor: "default" }}>
              <span />
              <span>Tarefa</span>
              <span>Agente</span>
              <span>Situação</span>
              <span>Conta</span>
            </div>
            {tasks.length === 0 ? <div className="empty">Nenhuma tarefa no journal nem membro no snapshot do squad.</div> : null}
            {tasks.map((t, i) => {
              const mark = taskSym(t.state);
              return (
                <button key={t.key} type="button" className={i === sel ? "task-grid task-sel" : "task-grid"} onClick={() => setSel(i)}>
                  <span className="mono" style={{ color: mark.c, fontWeight: 600 }}>
                    {mark.s}
                  </span>
                  <span style={{ fontWeight: i === sel ? 600 : 500 }}>{t.name}</span>
                  <span style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <Avatar name={t.role} size="xs" />
                    <span style={{ fontSize: 13 }}>{t.role}</span>
                  </span>
                  <span>
                    <Badge tone={missionTone(t.state || "run", attn && i === sel ? "1" : "")} dot>
                      {t.state || (live ? "executando" : "sem sessão")}
                    </Badge>
                  </span>
                  <span className="mono" style={{ fontSize: 12, color: "var(--text-secondary)" }}>
                    {t.account}
                  </span>
                </button>
              );
            })}
          </section>
          <aside className="detail">
            <div style={{ display: "flex", justifyContent: "space-between" }}>
              <span className="caps">Detalhe</span>
              {selected ? (
                <Badge tone="neutral" dot>
                  {selected.state || "—"}
                </Badge>
              ) : null}
            </div>
            <div style={{ fontSize: 17, fontWeight: 600 }}>{selected?.name || "—"}</div>
            <dl>
              <dt>Agente</dt>
              <dd>{selected?.role || "—"}</dd>
              <dt>CLI · modelo</dt>
              <dd className="mono" style={{ fontSize: 12.5 }}>
                {selected?.cli || "—"} · {selected?.model}
              </dd>
              <dt>Conta · política</dt>
              <dd className="mono" style={{ fontSize: 12.5 }}>
                {selected?.account} · conta fixa
              </dd>
              <dt>Tentativa</dt>
              <dd>{watch?.detail?.last ? "journal" : "—"}</dd>
              <dt>Último evento</dt>
              <dd>{watch?.detail?.last || "—"}</dd>
            </dl>
            {attn ? (
              <div style={{ display: "flex", flexDirection: "column", gap: 10, padding: 12, borderRadius: 10, background: "var(--surface-sunken)", border: "1px solid var(--border-default)" }}>
                <div style={{ fontSize: 13 }}>{watch?.attention?.text || attn}</div>
                <Input value={answer} onChange={(e) => setAnswer(e.target.value)} placeholder="Resposta para o journal" />
                <div className="inline-actions">
                  <Button
                    size="sm"
                    disabled={!answer || !mission}
                    onClick={async () => {
                      await api("/v0/watch/answer", {
                        method: "POST",
                        body: JSON.stringify({ workspace: mission?.workspace_id, task: watch?.attention?.task || selected?.task || "", text: answer }),
                      });
                      setAnswer("");
                      onRefresh();
                    }}
                  >
                    Enviar resposta
                  </Button>
                </div>
              </div>
            ) : null}
            <div style={{ fontSize: 12, color: "var(--text-muted)" }}>Contagem de tokens e volume de log não indicam conclusão. Andamento vem dos gates verificados.</div>
          </aside>
        </div>
      )}
      {layout === "grid" && (
        <div className="grid-cards">
          {tasks.map((t, i) => (
            <button key={t.key} type="button" className={i === sel ? "agent-card agent-card--sel" : "agent-card"} onClick={() => setSel(i)}>
              <strong>{t.name}</strong>
              <span className="muted">
                {t.role} · {t.account}
              </span>
              <div className="log-tail">{(watch?.log_lines || []).slice(-3).join("\n") || "sem cauda de log"}</div>
            </button>
          ))}
        </div>
      )}
      {layout === "terminal" && (
        <div className="chips">
          {tasks.map((t, i) => (
            <button key={t.key} type="button" className={i === sel ? "chip chip--sel" : "chip"} onClick={() => setSel(i)}>
              <Avatar name={t.role} size="xs" />
              {t.role} · {t.name}
            </button>
          ))}
        </div>
      )}
      <section className={layout === "terminal" ? "log-panel log-panel--fill" : "log-panel"}>
        <div className="log-head">
          <Tabs
            active={logTab}
            onChange={setLogTab}
            items={[
              { id: "log", label: "Log" },
              { id: "criteria", label: "Critérios" },
              { id: "diff", label: "Diff" },
              { id: "history", label: "Histórico" },
            ]}
          />
          <div className="inline-actions">
            {live ? (
              owns ? (
                <>
                  <span className="owner-accent">você controla a entrada</span>
                  <Button variant="secondary" size="sm" onClick={async () => {
                    await api(`/v0/sessions/${live.id}/control`, { method: "POST", body: JSON.stringify({ surface: "tui", client }) });
                    onRefresh();
                  }}>
                    Devolver controle
                  </Button>
                </>
              ) : (
                <>
                  <span className="owner-muted">{live.owner === "tui" ? "TUI controla a entrada" : "outro cliente controla"}</span>
                  <Button
                    size="sm"
                    onClick={async () => {
                      await api(`/v0/sessions/${live.id}/control`, { method: "POST", body: JSON.stringify({ surface: "web", client }) });
                      onRefresh();
                    }}
                  >
                    Assumir controle
                  </Button>
                </>
              )
            ) : (
              <span className="owner-muted">sem sessão viva</span>
            )}
          </div>
        </div>
        {logTab === "log" && live && (layout === "terminal" || layout === "list") ? (
          <>
            {!owns ? <div className="readonly-note">somente leitura — a TUI ou outro cliente controla a entrada</div> : null}
            <XTerm sessionId={live.id} inputEnabled={!!owns} client={client} />
          </>
        ) : null}
        {logTab === "log" && !live ? <div className="empty">Inicie uma sessão para o log ao vivo. Fechar este cliente não encerra o processo.</div> : null}
        {logTab === "criteria" && (
          <div>
            {crit.length === 0 ? <div className="empty">Sem critérios até existir um journal Batuta neste workspace.</div> : null}
            {crit.map((c) => (
              <div key={c.index} className={c.state === "pending" ? "crit crit--pending" : "crit"}>
                <span>{c.state === "pass" ? "✓" : c.state === "fail" ? "✗" : "?"}</span>
                <div>
                  <strong>
                    C{c.index} {c.title}
                  </strong>
                  <div className="muted">{c.state === "pass" ? "prova" : c.state === "fail" ? "revisão" : "pendente"}</div>
                </div>
              </div>
            ))}
          </div>
        )}
        {logTab === "diff" && (
          <div className="empty">
            Sem diff até a integração Batuta publicar uma revisão. O Console não inventa +/− a partir do log.
          </div>
        )}
        {logTab === "history" && (
          <div>
            {activity.slice(0, 12).map((e) => {
              const origin = eventOrigin(e.kind);
              return (
                <div key={e.seq} className="hist-grid">
                  <span className="mono muted">{clock(e.at)}</span>
                  <span style={{ fontWeight: 500 }}>{origin.label}</span>
                  <span>{e.kind}</span>
                </div>
              );
            })}
            {activity.length === 0 ? <div className="empty">Nenhum evento ainda.</div> : null}
          </div>
        )}
      </section>
    </div>
  );
}

function XTerm({ sessionId, inputEnabled, client }: { sessionId: string; inputEnabled: boolean; client: string }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const term = new Terminal({
      convertEol: true,
      fontFamily: "IBM Plex Mono, monospace",
      fontSize: 12,
      cursorBlink: inputEnabled,
      theme: { background: "#0D1117", foreground: "#E6EDF3" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    if (host.current) term.open(host.current);
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const token = localStorage.getItem("bt-token") || "";
    const ws = new WebSocket(`${proto}://${location.host}/v0/sessions/${sessionId}/terminal?token=${encodeURIComponent(token)}&client=${encodeURIComponent(client)}`);
    ws.binaryType = "arraybuffer";
    const sendResize = () => {
      if (!inputEnabled || ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") term.write(ev.data);
      else term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onopen = () => {
      fit.fit();
      sendResize();
    };
    const subData = term.onData((d) => {
      if (!inputEnabled) return;
      if (ws.readyState === WebSocket.OPEN) ws.send(d);
    });
    const subResize = term.onResize(({ cols, rows }) => {
      if (!inputEnabled || ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify({ type: "resize", cols, rows }));
    });
    const onWin = () => fit.fit();
    window.addEventListener("resize", onWin);
    const ro = host.current && typeof ResizeObserver !== "undefined" ? new ResizeObserver(() => fit.fit()) : null;
    if (host.current && ro) ro.observe(host.current);
    return () => {
      window.removeEventListener("resize", onWin);
      ro?.disconnect();
      subData.dispose();
      subResize.dispose();
      ws.close();
      term.dispose();
    };
  }, [sessionId, inputEnabled, client]);
  return <div className="xterm-host" ref={host} />;
}

function Squads({
  squads,
  skills,
  skillsRoot,
  onCreate,
  onEdit,
  onDuplicate,
}: {
  squads: Squad[];
  skills: Skill[];
  skillsRoot: string;
  onCreate: () => void;
  onEdit: (q: Squad) => void;
  onDuplicate: (q: Squad) => void;
}) {
  const batuta = squads.filter((q) => q.origin === "batuta");
  const custom = squads.filter((q) => q.origin !== "batuta");
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Squads</h1>
          <p className="sub">Salvar não executa nada. Os squads Batuta já vêm com papéis e skills; crie o seu quando o formato for outro.</p>
        </div>
        <Button onClick={onCreate}>Criar squad</Button>
      </div>
      {!skillsRoot ? (
        <Alert tone="warning" title="Catálogo sem disco">
          Skills oficiais ainda aparecem aqui. A injeção só ativa BATUTA_SKILLS quando o diretório existir.
        </Alert>
      ) : null}
      <SquadGroup title="Squads Batuta" hint="Catálogo oficial. Edite à vontade; duplicar cria um squad seu." squads={batuta} onEdit={onEdit} onDuplicate={onDuplicate} empty="O daemon ainda não semeou o catálogo." />
      <SquadGroup title="Seus squads" hint="Crie do zero ou duplique um Batuta." squads={custom} onEdit={onEdit} onDuplicate={onDuplicate} empty="Nenhum squad próprio ainda. Use Criar squad." />
      <section className="panel">
        <div className="panel-h">
          <span>Skills Batuta</span>
          <span className="mono muted" style={{ fontSize: 12 }}>
            {skillsRoot || "sem diretório"}
          </span>
        </div>
        <div className="skill-catalog">
          {skills.map((sk) => (
            <div key={sk.id} className="skill-card">
              <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
                <span className="mono" style={{ fontWeight: 600 }}>
                  {sk.id}
                </span>
                <Badge tone={sk.origin === "batuta" ? "accent" : "neutral"}>{sk.origin === "batuta" ? "Batuta" : "própria"}</Badge>
              </div>
              <div style={{ fontSize: 13, color: "var(--text-secondary)" }}>{sk.summary || "—"}</div>
              <div className="path">{sk.installed ? "instalada neste host" : "listada · ainda não está no disco"}</div>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

function SquadGroup({
  title,
  hint,
  squads,
  empty,
  onEdit,
  onDuplicate,
}: {
  title: string;
  hint: string;
  squads: Squad[];
  empty: string;
  onEdit: (q: Squad) => void;
  onDuplicate: (q: Squad) => void;
}) {
  return (
    <section className="squad-group">
      <div className="squad-group__head">
        <h2>{title}</h2>
        <p>{hint}</p>
      </div>
      {squads.length === 0 ? <div className="empty">{empty}</div> : null}
      <div className="cards">
        {squads.map((q) => (
          <button key={q.id} type="button" className="sq-card" onClick={() => onEdit(q)}>
            <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
              <span style={{ fontSize: 16, fontWeight: 600 }}>{q.name || "Sem nome"}</span>
              <span className="inline-actions">
                {q.origin === "batuta" ? <Badge tone="accent">Batuta</Badge> : <Badge tone="neutral">seu</Badge>}
                <Badge tone={q.draft ? "warning" : "success"}>{q.draft ? "rascunho" : `válido · v${q.version}`}</Badge>
              </span>
            </div>
            <div style={{ fontSize: 13, color: "var(--text-secondary)" }}>{q.purpose || "Sem propósito ainda."}</div>
            <div className="inline-actions">
              {(q.members || []).map((m) => (
                <span key={m.id || m.role} className="role-chip">
                  {m.role || "papel"}
                </span>
              ))}
            </div>
            <div className="path" style={{ display: "flex", justifyContent: "space-between" }}>
              <span>
                v{q.version} · {plural(q.members?.length || 0, "membro", "membros")}
              </span>
              <span
                className="linkish"
                onClick={(e) => {
                  e.stopPropagation();
                  onDuplicate(q);
                }}
              >
                duplicar
              </span>
            </div>
          </button>
        ))}
      </div>
    </section>
  );
}

function SquadEditor({
  squad,
  accounts,
  skills,
  workspaceId,
  onClose,
  onSaved,
}: {
  squad: Squad;
  accounts: Account[];
  skills: Skill[];
  workspaceId?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(squad.name);
  const [purpose, setPurpose] = useState(squad.purpose || "");
  const [members, setMembers] = useState<Member[]>(squad.members || []);
  const [msg, setMsg] = useState("");
  const [tone, setTone] = useState<"success" | "warning" | "danger" | "">("");

  function patch(i: number, part: Partial<Member>) {
    setMembers((ms) => ms.map((m, j) => (j === i ? { ...m, ...part } : m)));
    setMsg("");
  }

  const draft: Squad = { ...squad, name, purpose, members, origin: squad.origin || "custom" };
  const claudePending = members.some((m) => {
    const acc = accounts.find((a) => a.id === m.account_id);
    return acc?.isolation === "not_yet_qualified";
  });

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <p className="muted mono" style={{ margin: 0 }}>
            Squads / {name || "sem nome"}
          </p>
          <div className="inline-actions">
            <h1>{name || "Editor de squad"}</h1>
            {squad.origin === "batuta" ? <Badge tone="accent">Batuta</Badge> : <Badge tone="neutral">seu</Badge>}
            <Badge tone="neutral">{squad.draft ? `Rascunho · v${squad.version}` : `v${squad.version}`}</Badge>
          </div>
        </div>
        <div className="inline-actions">
          <Button variant="secondary" onClick={onClose}>
            Voltar
          </Button>
        </div>
      </div>
      <div className="cards-wide">
        <Field label="Nome">
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Propósito">
          <Input value={purpose} onChange={(e) => setPurpose(e.target.value)} />
        </Field>
      </div>
      {members.length === 0 ? <Alert tone="danger" title="squad vazio">Adicione um maestro antes de salvar.</Alert> : null}
      {claudePending ? <Alert tone="warning" title="isolamento não qualificado">Uma conta Claude nesta linha ainda não foi qualificada.</Alert> : null}
      {msg ? <Alert tone={tone || "info"} title={msg} /> : null}
      {members.length === 0 ? <div className="panel empty">Nenhum membro ainda…</div> : null}
      <section className="panel editor-panel">
        <table className="editor-table">
          <thead>
            <tr>
              <th>Papel</th>
              <th>CLI</th>
              <th>Modelo</th>
              <th>Esforço</th>
              <th>Conta / política</th>
              <th>Skills</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {members.map((mb, i) => {
              const incomplete = !mb.role || !mb.cli || !mb.account_id;
              const acc = accounts.find((a) => a.id === mb.account_id);
              const iso = acc ? isolationLabel(acc.isolation) : null;
              return (
                <tr key={mb.id || i} className={incomplete ? "row-warn" : undefined}>
                  <td>
                    <Input value={mb.role} onChange={(e) => patch(i, { role: e.target.value })} />
                    {mb.maestro ? (
                      <Badge tone="accent">maestro</Badge>
                    ) : (
                      <button type="button" className="linkish" onClick={() => setMembers((ms) => ms.map((m, j) => ({ ...m, maestro: j === i })))}>
                        definir como maestro
                      </button>
                    )}
                  </td>
                  <td>
                    <Select value={mb.cli} onChange={(e) => patch(i, { cli: e.target.value })}>
                      <option value="codex">Codex</option>
                      <option value="claude">Claude</option>
                      <option value="opencode">OpenCode</option>
                      <option value="gemini">Gemini</option>
                    </Select>
                  </td>
                  <td>
                    <Input value={mb.model || ""} onChange={(e) => patch(i, { model: e.target.value })} placeholder="herdar" />
                  </td>
                  <td>
                    <Select value={mb.effort || "medium"} onChange={(e) => patch(i, { effort: e.target.value })}>
                      <option value="low">baixo</option>
                      <option value="medium">médio</option>
                      <option value="high">alto</option>
                    </Select>
                  </td>
                  <td>
                    <Select value={mb.account_id} onChange={(e) => patch(i, { account_id: e.target.value })}>
                      <option value="">sem conta</option>
                      {accounts.map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.label}
                        </option>
                      ))}
                    </Select>
                    <div className="muted" style={{ fontSize: 11.5, marginTop: 4 }}>
                      {acc ? `conta fixa · ${iso?.text}` : "sem conta"}
                    </div>
                  </td>
                  <td>
                    <SkillPicker value={mb.skills || []} catalog={skills} onChange={(next) => patch(i, { skills: next })} />
                  </td>
                  <td>
                    <IconButton label="Remover" onClick={() => setMembers((ms) => ms.filter((_, j) => j !== i))}>
                      ×
                    </IconButton>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </section>
      <div className="inline-actions">
        <Button
          variant="soft"
          onClick={() =>
            setMembers((ms) => [...ms, { role: "", cli: "codex", account_id: accounts[0]?.id || "", maestro: ms.length === 0, effort: "medium" }])
          }
        >
          Adicionar membro
        </Button>
        <span className="muted">{plural(members.length, "membro", "membros")}</span>
        <span style={{ flex: 1 }} />
        <Button
          variant="secondary"
          onClick={async () => {
            const v = await api<{ validation: { title: string; text: string; ready: boolean; tone: string } }>(`/v0/squads/${squad.id}/validate`, {
              method: "POST",
              body: JSON.stringify(draft),
            });
            setTone((v.validation.tone as "success" | "warning" | "danger") || "info");
            setMsg(`${v.validation.title}. ${v.validation.text}`);
          }}
        >
          Validar
        </Button>
        {workspaceId ? (
          <Button
            variant="secondary"
            onClick={async () => {
              const saved = await api<Squad>("/v0/squads", { method: "POST", body: JSON.stringify(draft) });
              await api("/v0/missions", { method: "POST", body: JSON.stringify({ workspace_id: workspaceId, title: name || "Nova missão", squad_id: saved.id }) });
              onSaved();
            }}
          >
            Criar missão com este squad
          </Button>
        ) : null}
        <Button
          onClick={async () => {
            await api<Squad>("/v0/squads", { method: "POST", body: JSON.stringify(draft) });
            onSaved();
          }}
        >
          Salvar versão
        </Button>
      </div>
      <div className="info-cards">
        <div className="panel" style={{ padding: "var(--cardpad)" }}>
          <h2 style={{ margin: "0 0 8px", fontSize: 16 }}>Concorrência</h2>
          <p className="sub">A capacidade é da conta, não do squad. Duas missões na mesma conta respeitam o teto configurado.</p>
        </div>
        <div className="panel" style={{ padding: "var(--cardpad)" }}>
          <h2 style={{ margin: "0 0 8px", fontSize: 16 }}>Precedência</h2>
          <p className="sub">
            <span className="mono">workspace › missão › membro › squad</span>. Este editor grava o membro e o squad; overrides de workspace/missão ainda não são armazenados.
          </p>
        </div>
      </div>
    </div>
  );
}

function Accounts({
  accounts,
  limits,
  sessions,
  open,
  onOpen,
  onClose,
  onSaved,
}: {
  accounts: Account[];
  limits: Limit[];
  sessions: Session[];
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  onSaved: () => void;
}) {
  const live = sessions.filter((s) => s.process === "running");
  const cap = accounts.reduce((n, a) => n + (a.session_cap || 0), 0);
  const latest = limits.map((l) => l.observed_at || l.last_attempt).filter(Boolean) as string[];
  const newest = latest.sort().at(-1);
  const pending = accounts.filter((a) => a.isolation !== "supported").length;
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Contas e limites</h1>
          <p className="sub">Credenciais ficam no cofre do sistema — nunca aqui.</p>
        </div>
        <Button onClick={onOpen}>Adicionar conta</Button>
      </div>
      <div className="stats">
        <div className="stat-card">
          <Stat label="Contas" value={accounts.length} />
        </div>
        <div className="stat-card">
          <Stat label="Sessões ativas" value={`${live.length}${cap ? ` / ${cap}` : ""}`} />
        </div>
        <div className="stat-card">
          <Stat label="Coleta de limites" value={newest ? relTime(newest) : "nunca"} />
        </div>
        <div className="stat-card">
          <Stat label="Pendências" value={pending} />
        </div>
      </div>
      <section className="panel">
        <div className="acct-grid caps" style={{ cursor: "default" }}>
          <span>Conta</span>
          <span>Provedor · CLI</span>
          <span>Isolamento</span>
          <span>Sessões</span>
          <span>Limite conhecido</span>
          <span>Fonte</span>
        </div>
        {accounts.map((a) => {
          const lim = limits.find((l) => l.account_id === a.id);
          const iso = isolationLabel(a.isolation);
          const used = live.filter((s) => s.account_id === a.id).length;
          return (
            <div key={a.id} className="acct-grid">
              <span>
                <span className="mono" style={{ fontWeight: 500 }}>
                  {a.connected ? "● " : "○ "}
                  {a.label}
                </span>
              </span>
              <span>
                {a.provider} · {a.adapter}
              </span>
              <span>
                <Badge tone={iso.tone}>{iso.text}</Badge>
              </span>
              <span className="mono">
                {used} / {a.session_cap || "—"}
              </span>
              <span>
                <div className="muted">{lim?.percent == null ? "desconhecido" : `${Math.round(lim.percent)}% · ${lim.window || "janela"}`}</div>
                <Progress value={lim?.percent} unknown={lim?.percent == null} />
              </span>
              <span className="mono muted">{lim?.origin || lim?.source || "—"}</span>
            </div>
          );
        })}
        {accounts.length === 0 ? <div className="empty">Nenhuma conta ainda.</div> : null}
      </section>
      <p className="muted">Limite da conta ≠ tokens da sessão ≠ custo. Dado ausente permanece desconhecido — nunca 0%.</p>
      <AddAccount open={open} onClose={onClose} onSaved={onSaved} />
    </div>
  );
}

function AddAccount({ open, onClose, onSaved }: { open: boolean; onClose: () => void; onSaved: () => void }) {
  const [step, setStep] = useState(1);
  const [label, setLabel] = useState("");
  const [adapter, setAdapter] = useState("codex");
  const [draft, setDraft] = useState<Account | null>(null);
  const [loading, setLoading] = useState(false);
  const isolation =
    adapter === "codex" || adapter === "opencode" ? "supported" : adapter === "gemini" ? "unsupported" : "not_yet_qualified";
  const iso = isolationLabel(isolation);
  const providers = [
    { id: "codex", title: "OpenAI · Codex", hint: "HOME isolado por conta; login oficial no PTY." },
    { id: "claude", title: "Anthropic · Claude", hint: "Pasta isolada; login ainda não qualificado." },
    { id: "opencode", title: "OpenCode · chave API", hint: "Isolamento suportado; a chave não passa por esta UI." },
    { id: "gemini", title: "Google · Gemini", hint: "Isolamento sem suporte nesta versão." },
  ];
  return (
    <Drawer
      open={open}
      title="Adicionar conta"
      onClose={onClose}
      footer={
        step === 1 ? (
          <>
            <Button variant="secondary" onClick={onClose}>
              Cancelar
            </Button>
            <Button disabled={!label} onClick={() => setStep(2)}>
              Continuar
            </Button>
          </>
        ) : step === 2 ? (
          <>
            <Button variant="secondary" onClick={() => setStep(1)}>
              Voltar
            </Button>
            <Button
              loading={loading}
              full
              onClick={async () => {
                setLoading(true);
                try {
                  const a = await api<Account>("/v0/accounts", { method: "POST", body: JSON.stringify({ label, adapter }) });
                  const connected = await api<Account>(`/v0/accounts/${a.id}/connect`, { method: "POST", body: "{}" });
                  setDraft(connected);
                  setStep(3);
                } finally {
                  setLoading(false);
                }
              }}
            >
              Conectar com o mecanismo oficial
            </Button>
          </>
        ) : (
          <Button onClick={onSaved}>Salvar conta</Button>
        )
      }
    >
      <Stepper steps={["Provedor", "Conectar", "Confirmar"]} current={step} />
      {step === 1 && (
        <div className="radio-col" style={{ marginTop: 16 }}>
          {providers.map((p) => (
            <label key={p.id} className={adapter === p.id ? "choice choice--on" : "choice"}>
              <input type="radio" className="lyra-radio" checked={adapter === p.id} onChange={() => setAdapter(p.id)} />
              <span>
                <strong>{p.title}</strong>
                <div className="muted" style={{ fontSize: 12.5 }}>
                  {p.hint}
                </div>
              </span>
            </label>
          ))}
          <Field label="Nome da conta" hint="Rótulo local — não é e-mail nem token.">
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="trabalho" />
          </Field>
          <Badge tone={iso.tone}>{iso.text}</Badge>
        </div>
      )}
      {step === 2 && (
        <div style={{ marginTop: 16, display: "flex", flexDirection: "column", gap: 12 }}>
          <p>
            {label} · {adapter}
          </p>
          <p className="muted">
            Cria um HOME isolado para esta conta. O login oficial acontece no PTY da sessão, com esse HOME — credenciais nunca passam por esta API. Não desconecta nem substitui as existentes.
          </p>
        </div>
      )}
      {step === 3 && draft && (
        <div style={{ marginTop: 16 }}>
          <Alert tone="success" title="Conectada">
            Isolamento {draft.isolation}. Inicie uma sessão para completar o login oficial no PTY.
          </Alert>
          <dl className="detail" style={{ border: 0, padding: "12px 0" }}>
            <dt>Conta</dt>
            <dd>{draft.label}</dd>
            <dt>Identidade</dt>
            <dd className="mono">{draft.vault_ref ? `${draft.vault_ref.slice(0, 18)}…` : "—"}</dd>
            <dt>Isolamento</dt>
            <dd>{draft.isolation}</dd>
            <dt>Sessões</dt>
            <dd>{draft.session_cap || "—"}</dd>
          </dl>
        </div>
      )}
    </Drawer>
  );
}

function ActivityScreen({
  activity,
  sessions,
  missions,
  onOpenMission,
}: {
  activity: Event[];
  sessions: Session[];
  missions: Mission[];
  onOpenMission: (m: Mission) => void;
}) {
  const [filter, setFilter] = useState("all");
  const cursor = activity[0]?.seq || 0;
  const filtered = activity.filter((e) => {
    if (filter === "all") return true;
    if (filter === "sessions") return e.kind.startsWith("session");
    if (filter === "deliveries") return e.kind.includes("watch") || e.kind.includes("delivery");
    if (filter === "limits") return e.kind.includes("limit") || e.kind.startsWith("account");
    if (filter === "clients") return e.kind.includes("control") || e.kind.includes("client");
    return true;
  });
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Atividade</h1>
          <p className="sub">
            ao vivo · cursor <span className="mono">{cursor}</span>
          </p>
        </div>
      </div>
      <Tabs
        active={filter}
        onChange={setFilter}
        items={[
          { id: "all", label: "Tudo" },
          { id: "sessions", label: "Sessões" },
          { id: "deliveries", label: "Entregas" },
          { id: "limits", label: "Contas e limites" },
          { id: "clients", label: "Clientes" },
        ]}
      />
      <div className="cards-wide">
        <section className="panel">
          {filtered.map((e) => {
            const origin = eventOrigin(e.kind);
            const mid = typeof e.payload?.mission_id === "string" ? e.payload.mission_id : "";
            const mission = missions.find((m) => m.id === mid);
            return (
              <button
                key={e.seq}
                type="button"
                className="act-row"
                onClick={() => {
                  if (mission) onOpenMission(mission);
                }}
              >
                <span className="mono muted">{clock(e.at)}</span>
                <span>
                  <Badge tone={origin.tone}>{origin.label}</Badge>
                </span>
                <span>
                  <strong>{e.kind}</strong>
                  {mission ? <div className="mono muted">missão: {mission.title}</div> : null}
                </span>
              </button>
            );
          })}
          {filtered.length === 0 ? <div className="empty">Nenhum evento neste filtro.</div> : null}
        </section>
        <section className="panel">
          <div className="panel-h">Sessões</div>
          <p className="empty" style={{ paddingBottom: 0 }}>
            Desconectar um cliente não encerra o processo.
          </p>
          <div className="acct-grid caps" style={{ gridTemplateColumns: "1.2fr 1fr 1fr 1fr" }}>
            <span>Sessão</span>
            <span>Processo</span>
            <span>Cliente</span>
            <span>Trabalho</span>
          </div>
          {sessions.map((s) => (
            <div key={s.id} className="acct-grid" style={{ gridTemplateColumns: "1.2fr 1fr 1fr 1fr" }}>
              <span className="mono">{s.id.slice(0, 10)}</span>
              <span>
                {s.process}
                {s.process === "unknown" ? <div className="muted">{processLife(s.process)}</div> : null}
              </span>
              <span>{s.owner}</span>
              <span>{s.work || "—"}</span>
            </div>
          ))}
        </section>
      </div>
    </div>
  );
}

function Review({ watch, mission, onBack }: { watch: Watch | null; mission: Mission | null; onBack: () => void }) {
  const crit = criteria(watch);
  const ev = evidence(watch);
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <p className="muted mono" style={{ margin: 0 }}>
            {mission?.title || "Missão"} / Estado da entrega
          </p>
          <div className="inline-actions">
            <h1>{watch?.delivery || mission?.title || "Revisão"}</h1>
            <Badge tone={watch?.available ? "success" : "neutral"} dot>
              {watch?.available ? "Com journal" : "Sem journal"}
            </Badge>
          </div>
          <p className="sub">Ações vêm da integração Batuta. O Console não edita o journal.</p>
        </div>
        <div className="inline-actions">
          <Button variant="secondary" onClick={onBack}>
            Voltar à sala
          </Button>
          <Button variant="secondary" disabled title="Reabrir tarefa exige a integração Batuta">
            Reabrir tarefa
          </Button>
        </div>
      </div>
      <div className="cards-wide">
        <section className="panel">
          <div className="panel-h">Diff</div>
          <div className="empty">Sem arquivos de revisão publicados. Nada de +/− fictício.</div>
        </section>
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--gap)" }}>
          <section className="panel">
            <div className="panel-h">Critérios</div>
            {crit.length === 0 ? <div className="empty">Sem critérios neste journal.</div> : null}
            {crit.map((c) => (
              <div key={c.index} className={c.state === "pending" ? "crit crit--pending" : "crit"}>
                <span>{c.state === "pass" ? "✓" : "?"}</span>
                <div>
                  <strong>{c.title}</strong>
                  <div className="muted">{c.state}</div>
                </div>
              </div>
            ))}
          </section>
          <section className="panel">
            <div className="panel-h">Evidências</div>
            {ev.length === 0 ? <div className="empty">Sem evidência até existir journal Batuta.</div> : null}
            {ev.map((e, i) => (
              <div key={i} className="hist-grid" style={{ gridTemplateColumns: "90px minmax(0,1fr) auto" }}>
                <span className="caps">{e.kind}</span>
                <span>{e.text}</span>
                <span className="muted">{e.origin}</span>
              </div>
            ))}
          </section>
        </div>
      </div>
    </div>
  );
}

function CreateWorkspaceDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (w: Workspace) => void;
}) {
  const [name, setName] = useState("");
  const [root, setRoot] = useState("");
  const slug = slugify(name);
  return (
    <Dialog
      open={open}
      title="Novo workspace"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancelar
          </Button>
          <Button
            disabled={!name || !root}
            onClick={async () => {
              const w = await api<Workspace>("/v0/workspaces", { method: "POST", body: JSON.stringify({ name, root }) });
              setName("");
              setRoot("");
              onCreated(w);
            }}
          >
            Criar
          </Button>
        </>
      }
    >
      <div className="lyra-wscreate">
        <Field label="Nome">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="acme" />
        </Field>
        <div>
          <span className="lyra-label">Caminho</span>
          <div className="lyra-wscreate__slug" style={{ marginTop: 8 }}>
            <span className="lyra-wscreate__slug-prefix">~/dev/</span>
            <input className="lyra-wscreate__slug-input" value={root} onChange={(e) => setRoot(e.target.value)} placeholder={slug || "caminho absoluto"} />
          </div>
          <p className="lyra-hint">Use o caminho absoluto do checkout. O prefixo é só referência visual.</p>
        </div>
      </div>
    </Dialog>
  );
}

function SkillPicker({
  value,
  catalog,
  onChange,
}: {
  value: string[];
  catalog: Skill[];
  onChange: (next: string[]) => void;
}) {
  const [extra, setExtra] = useState("");
  function toggle(id: string) {
    onChange(value.includes(id) ? value.filter((x) => x !== id) : [...value, id]);
  }
  return (
    <div className="skill-picker">
      {value.map((id) => (
        <button key={id} type="button" className="skill-chip skill-chip--on" onClick={() => toggle(id)} title="remover">
          {id} ×
        </button>
      ))}
      <details className="skill-add">
        <summary>+ skill</summary>
        <div className="skill-add__pop">
          <div className="skill-add__label">Batuta</div>
          {catalog
            .filter((sk) => sk.origin === "batuta")
            .map((sk) => (
              <button
                key={sk.id}
                type="button"
                title={sk.summary}
                className={value.includes(sk.id) ? "skill-chip skill-chip--on" : "skill-chip skill-chip--off"}
                onClick={() => toggle(sk.id)}
              >
                {sk.id}
              </button>
            ))}
          {catalog.some((sk) => sk.origin !== "batuta") ? <div className="skill-add__label">No disco</div> : null}
          {catalog
            .filter((sk) => sk.origin !== "batuta")
            .map((sk) => (
              <button
                key={sk.id}
                type="button"
                title={sk.summary}
                className={value.includes(sk.id) ? "skill-chip skill-chip--on" : "skill-chip skill-chip--off"}
                onClick={() => toggle(sk.id)}
              >
                {sk.id}
              </button>
            ))}
          <input
            className="skill-custom"
            value={extra}
            placeholder="nome da skill própria"
            onChange={(e) => setExtra(e.target.value)}
            onBlur={() => {
              const id = extra.trim();
              if (id && !value.includes(id)) onChange([...value, id]);
              setExtra("");
            }}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              e.preventDefault();
              const id = extra.trim();
              if (id && !value.includes(id)) onChange([...value, id]);
              setExtra("");
            }}
          />
        </div>
      </details>
    </div>
  );
}
