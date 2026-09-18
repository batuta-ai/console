import { useEffect, useMemo, useRef, useState } from "react";
import { useTheme } from "@lyra-ds/react/theme-provider";
import { Activity, FolderOpen, House, KeyRound, Moon, Sun, Target, Users } from "lucide-react";
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
import { T } from "./i18n";
import { Alert, AppSidebar, Avatar, Badge, BottomNav, Brand, Breadcrumb, Button, Card, Dialog, Drawer, EmptyState, Field, IconButton, Input, PageHeader, Progress, Select, Shell, Stat, Stepper, Tabs, WorkspaceSwitcher } from "./ui";

const webClient = webClientID();

const NAV: { id: Screen; label: () => string; icon: typeof House }[] = [
  { id: "home", label: () => T("home"), icon: House },
  { id: "missions", label: () => T("missions"), icon: Target },
  { id: "squads", label: () => T("squads"), icon: Users },
  { id: "accounts", label: () => T("nav_accounts"), icon: KeyRound },
  { id: "activity", label: () => T("activity"), icon: Activity },
];

function useMQ(query: string) {
  const [ok, setOk] = useState(() => (typeof window !== "undefined" ? window.matchMedia(query).matches : false));
  useEffect(() => {
    const mm = window.matchMedia(query);
    const on = () => setOk(mm.matches);
    on();
    mm.addEventListener("change", on);
    window.addEventListener("resize", on);
    window.visualViewport?.addEventListener("resize", on);
    return () => {
      mm.removeEventListener("change", on);
      window.removeEventListener("resize", on);
      window.visualViewport?.removeEventListener("resize", on);
    };
  }, [query]);
  return ok;
}

function screenTitle(screen: Screen, mission: Mission | null, squad: Squad | null) {
  if (screen === "home") return T("home");
  if (screen === "missions") return T("missions");
  if (screen === "room") return mission?.title || T("room_title");
  if (screen === "squads") return T("squads");
  if (screen === "editor") return squad?.name || T("squad_editor");
  if (screen === "accounts") return T("nav_accounts");
  if (screen === "activity") return T("activity");
  return T("review_delivery");
}

export function App() {
  const phone = useMQ("(max-width: 720px)");
  const tablet = useMQ("(max-width: 1099px)");
  const rail = !phone && tablet;
  const { dark, toggle: toggleTheme } = useTheme();
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
  const chrome = chromeScreen(screen);
  const running = sessions.filter((s) => s.process === "running").length;

  const sidebar = (
    <AppSidebar
      brand={
        <>
          <Brand mark="/assets/batuta-symbol.svg" markDark="/assets/batuta-symbol-white.svg" size={28}>
            batuta / console
          </Brand>
          <div className="side-pad">
            <WorkspaceSwitcher
              workspaces={workspaces}
              current={currentWs}
              collapsed={rail}
              onChange={pickWorkspace}
              onCreate={() => setWsDialog(true)}
            />
          </div>
        </>
      }
      width={232}
      collapsible={!phone}
      collapsed={rail}
      labels={{ collapse: T("collapse_nav"), expand: T("expand_nav") }}
      groups={[
        {
          items: NAV.map((item) => {
            const Icon = item.icon;
            return {
              id: item.id,
              label: item.label(),
              icon: <Icon size={17} strokeWidth={2} />,
              active: chrome === item.id,
              badge: item.id === "missions" && attentionCount && screen !== "home" ? <span className="nav-count">{attentionCount}</span> : undefined,
              onSelect: () => go(item.id),
            };
          }),
        },
      ]}
      footer={
        <div className="side-foot">
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span className="live-dot" />
            <span className="foot-copy">{T("consoled_active")}</span>
          </div>
          <span className="foot-copy">{T("api_foot", { sessions: running, clients: liveClients })}</span>
        </div>
      }
      onSelect={(id) => go(id as Screen)}
    />
  );

  const topbar = (
    <>
      {phone || rail ? (
        <WorkspaceSwitcher
          workspaces={workspaces}
          current={currentWs}
          collapsed
          onChange={pickWorkspace}
          onCreate={() => setWsDialog(true)}
        />
      ) : null}
      <Breadcrumb aria-label={T("crumb_nav")} items={[{ label: currentWs?.name || "workspace" }, { label: screenTitle(screen, activeMission, editing) }]} />
      {attention ? (
        <button type="button" className="need-you" onClick={() => go("room")}>
          <span className="live-dot bt-pulse" style={{ background: "var(--warning)" }} />
          {T("needs_you_n", { n: 1 })}
        </button>
      ) : null}
      <IconButton label={T("toggle_theme")} onClick={toggleTheme}>
        {dark ? <Sun size={17} /> : <Moon size={17} />}
      </IconButton>
      <Avatar name="Batuta" size="sm" />
    </>
  );

  return (
    <div className={phone ? "frame frame--phone" : "frame"} data-theme={dark ? "dark" : "light"} data-density="comfortable">
      <Shell scroll="content" className="bt-shell" sidebar={!phone ? sidebar : undefined} sidebarWidth={rail ? 64 : 232} topbar={topbar} sidebarLabel="Batuta Console">
        {err ? <div className="err">{err}</div> : null}
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
              const q = await api<Squad>("/v0/squads", { method: "POST", body: JSON.stringify({ name: T("new_squad_name"), purpose: "", origin: "custom", members: [] }) });
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
                body: JSON.stringify({ name: `${q.name}${T("copy_suffix")}`, purpose: q.purpose || "", origin: "custom", members: q.members || [] }),
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
      </Shell>
      {phone ? (
        <BottomNav
          items={NAV.map((item) => {
            const Icon = item.icon;
            return {
              id: item.id,
              label: item.label(),
              icon: <Icon size={18} strokeWidth={2} />,
              active: chrome === item.id,
              onClick: () => go(item.id),
            };
          })}
          onSelect={(id) => go(id as Screen)}
        />
      ) : null}
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

function chromeScreen(screen: Screen): Screen {
  if (screen === "editor") return "squads";
  if (screen === "room" || screen === "review") return "missions";
  return screen;
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
    <div className="page" data-screen-label={T("home")}>
      <PageHeader
        title={greeting()}
        description={T(attention ? "home_sub_attention" : "home_sub", {
          n: active.length,
          deliveries: T(active.length === 1 ? "delivery_one" : "delivery_many"),
          plural: active.length === 1 ? "" : "s",
          ws: wsName,
        })}
        actions={
          <Button variant="secondary" onClick={onNewWorkspace}>
            {T("new_workspace")}
          </Button>
        }
      />
      {attention ? (
        <div className="banner">
          <span className="banner-dot bt-pulse" />
          <div style={{ flex: 1, minWidth: 0 }}>
            <strong style={{ color: "var(--warning-text)" }}>{T("needs_you")}</strong> <span>· {attention}</span>
          </div>
          <Button size="sm" onClick={onRoom}>
            {T("open_room")}
          </Button>
        </div>
      ) : null}
      <div className="cards">
        {workspaces.map((w) => {
          const n = missions.filter((m) => m.workspace_id === w.id && !/done|complete|conclu/i.test(m.status)).length;
          return (
            <Card asChild interactive key={w.id}>
              <button type="button" className="ws-card" onClick={() => onPickWorkspace(w.id)}>
                <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
                  <span className="mono-name">{w.name}</span>
                  <Badge tone={n ? "accent" : "neutral"} dot>
                    {n ? T("active_badge", { n }) : T("idle")}
                  </Badge>
                </div>
                <div className="path">{w.root}</div>
                <div style={{ fontSize: 13, color: "var(--text-secondary)" }}>
                  {n ? plural(n, "mission_progress_one", "mission_progress_many") : T("no_active_missions_ws")}
                </div>
              </button>
            </Card>
          );
        })}
        {workspaces.length === 0 ? <EmptyState title={T("no_workspaces")} /> : null}
      </div>
      <div className="cards-wide">
        <section className="panel">
          <div className="panel-h">
            <span>{T("active_deliveries")}</span>
            <button type="button" className="linkish" onClick={onMissions}>
              {T("view_missions")}
            </button>
          </div>
          {active.length === 0 ? <EmptyState title={T("no_active_deliveries")} /> : null}
          {active.map((m) => (
            <button key={m.id} type="button" className="panel-row" onClick={() => onOpenRoom(m)}>
              <span style={{ fontWeight: 500 }}>{m.title}</span>
              <span className="mono muted" style={{ fontSize: 12 }}>
                {m.id}
              </span>
              <span style={{ fontSize: 12.5, color: "var(--text-secondary)" }}>
                {m.squad_snapshot?.name || "—"} · {m.attention || T("follow_gates")}
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
          <div className="panel-h">{T("nav_accounts")}</div>
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
            {accounts.length === 0 ? <span className="muted">{T("no_accounts")}</span> : null}
            <button type="button" className="linkish" onClick={onAccounts}>
              {T("view_all_accounts")}
            </button>
            <span className="muted" style={{ fontSize: 12 }}>
              {T("sessions_live", { n: sessions.filter((s) => s.process === "running").length })}
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
      <PageHeader
        title={T("missions")}
        actions={
          <Button
            onClick={async () => {
              if (!workspace || !squads[0]) return alert(T("need_ws_squad"));
              await api("/v0/missions", { method: "POST", body: JSON.stringify({ workspace_id: workspace.id, title: T("new_mission_title"), squad_id: squads[0].id }) });
              onCreated();
            }}
          >
            {T("new_mission")}
          </Button>
        }
      />
      <Tabs
        active={filter}
        onChange={setFilter}
        items={[
          { id: "all", label: T("filter_all"), count: missions.length },
          { id: "active", label: T("filter_active"), count: active.length },
          { id: "done", label: T("filter_done"), count: done.length },
        ]}
      />
      <section className="panel">
        <div className="mission-grid caps" style={{ cursor: "default" }}>
          <span>{T("col_objective")}</span>
          <span>{T("col_squad")}</span>
          <span>{T("col_status")}</span>
          <span>{T("col_verified")}</span>
          <span>{T("col_next")}</span>
        </div>
        {shown.length === 0 ? <EmptyState title={T("no_missions_filter")} /> : null}
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
                  {pct == null ? T("no_journal") : T("criteria_pct", { pct })}
                </span>
                <Progress value={pct} unknown={pct == null} />
              </span>
              <span style={{ fontSize: 13, color: attn ? "var(--warning-text)" : undefined, fontWeight: attn ? 600 : 400 }}>
                {attn ? attention || m.attention : T("follow_gates")}
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
        role: members[i]?.role || T("agent_default"),
        state: r.state,
        account: accounts.find((a) => a.id === members[i]?.account_id)?.label || members[i]?.account_id || "—",
        cli: members[i]?.cli || "",
        model: members[i]?.model || T("inherit"),
        task: r.task,
        gates: r.gates,
      }))
    : members.map((mb, i) => ({
        key: mb.id || String(i),
        name: mb.role || T("member_default"),
        role: mb.role || T("agent_default"),
        state: "",
        account: accounts.find((a) => a.id === mb.account_id)?.label || mb.account_id || "—",
        cli: mb.cli,
        model: mb.model || T("inherit"),
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
      <PageHeader
        title={mission?.title || T("room_title")}
        description={
          T("room_sub", {
            id: mission?.id || "—",
            done: integrated,
            total: Math.max(tasks.length, 1),
            live: live ? T("running_one") : T("no_live_session"),
          }) + (shown?.process === "unknown" ? T("catalog_only_process") : "")
        }
        actions={
          <div className="inline-actions">
            {["G0", "G1", "G2", "G3"].map((g, i) => (
              <span key={g} className={gates[i] === "✓" ? "gate gate--done" : "gate gate--wait"}>
                {g} {gates[i]}
              </span>
            ))}
            <Button variant="secondary" onClick={onReview}>
              {T("review_delivery")}
            </Button>
            {mission && !live ? (
              <>
                <label className="inject-toggle">
                  <input type="checkbox" checked={inject} onChange={(e) => setInject(e.target.checked)} />
                  {T("inject_batuta")}
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
                  {T("start_session")}
                </Button>
              </>
            ) : null}
          </div>
        }
      >
        <div className="inline-actions">
          {watch?.delivery ? <Badge tone="accent">{String(watch.delivery)}</Badge> : <Badge tone="neutral">{T("no_batuta_journal")}</Badge>}
          {live?.batuta_injected ? <Badge tone="success">{T("batuta_injected")}</Badge> : live ? <Badge tone="neutral">{T("generic_cli")}</Badge> : null}
          {attn ? (
            <Badge tone="warning" dot>
              {T("needs_you_n", { n: 1 })}
            </Badge>
          ) : null}
        </div>
      </PageHeader>
      {inject && !skillsRoot && !live ? (
        <Alert tone="warning" title={T("skills_missing_title")}>
          {T("skills_missing_body")}
        </Alert>
      ) : null}
      {attn ? (
        <div className="banner">
          <span className="banner-dot bt-pulse" />
          <div style={{ flex: 1 }}>
            <strong style={{ color: "var(--warning-text)" }}>{T("needs_you")}</strong> · {attn} {T("answer_no_paid")}
          </div>
          <Button
            size="sm"
            onClick={() => {
              const i = tasks.findIndex((t) => t.task && t.task === watch?.attention?.task);
              if (i >= 0) setSel(i);
              setLogTab("log");
            }}
          >
            {T("answer")}
          </Button>
        </div>
      ) : null}
      {shown?.process === "unknown" ? <Alert tone="warning" title={T("recovered_history")}>{processLife("unknown")}</Alert> : null}
      <Tabs
        active={layout}
        onChange={(id) => setLayout(id as "list" | "grid" | "terminal")}
        items={[
          { id: "list", label: T("layout_list") },
          { id: "grid", label: T("layout_grid") },
          { id: "terminal", label: T("layout_terminal") },
        ]}
      />
      {layout === "list" && (
        <div className="cards-wide">
          <section className="panel">
            <div className="task-grid caps" style={{ cursor: "default" }}>
              <span />
              <span>{T("col_task")}</span>
              <span>{T("col_agent")}</span>
              <span>{T("col_status")}</span>
              <span>{T("col_account")}</span>
            </div>
            {tasks.length === 0 ? <div className="empty">{T("no_tasks")}</div> : null}
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
                      {t.state || (live ? T("executing") : T("no_session"))}
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
              <span className="caps">{T("detail")}</span>
              {selected ? (
                <Badge tone="neutral" dot>
                  {selected.state || "—"}
                </Badge>
              ) : null}
            </div>
            <div style={{ fontSize: 17, fontWeight: 600 }}>{selected?.name || "—"}</div>
            <dl>
              <dt>{T("agent")}</dt>
              <dd>{selected?.role || "—"}</dd>
              <dt>{T("cli_model")}</dt>
              <dd className="mono" style={{ fontSize: 12.5 }}>
                {selected?.cli || "—"} · {selected?.model}
              </dd>
              <dt>{T("account_policy")}</dt>
              <dd className="mono" style={{ fontSize: 12.5 }}>
                {selected?.account} · {T("fixed_account")}
              </dd>
              <dt>{T("attempt")}</dt>
              <dd>{watch?.detail?.last ? T("journal") : "—"}</dd>
              <dt>{T("last_event")}</dt>
              <dd>{watch?.detail?.last || "—"}</dd>
            </dl>
            {attn ? (
              <div style={{ display: "flex", flexDirection: "column", gap: 10, padding: 12, borderRadius: 10, background: "var(--surface-sunken)", border: "1px solid var(--border-default)" }}>
                <div style={{ fontSize: 13 }}>{watch?.attention?.text || attn}</div>
                <Input value={answer} onChange={(e) => setAnswer(e.target.value)} placeholder={T("answer_placeholder")} />
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
                    {T("send_answer")}
                  </Button>
                </div>
              </div>
            ) : null}
            <div style={{ fontSize: 12, color: "var(--text-muted)" }}>{T("tokens_not_done")}</div>
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
              <div className="log-tail">{(watch?.log_lines || []).slice(-3).join("\n") || T("no_log_tail")}</div>
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
              { id: "log", label: T("log") },
              { id: "criteria", label: T("criteria") },
              { id: "diff", label: "Diff" },
              { id: "history", label: T("history") },
            ]}
          />
          <div className="inline-actions">
            {live ? (
              owns ? (
                <>
                  <span className="owner-accent">{T("you_control")}</span>
                  <Button variant="secondary" size="sm" onClick={async () => {
                    await api(`/v0/sessions/${live.id}/control`, { method: "POST", body: JSON.stringify({ surface: "tui", client }) });
                    onRefresh();
                  }}>
                    {T("return_control")}
                  </Button>
                </>
              ) : (
                <>
                  <span className="owner-muted">{live.owner === "tui" ? T("tui_controls") : T("other_controls")}</span>
                  <Button
                    size="sm"
                    onClick={async () => {
                      await api(`/v0/sessions/${live.id}/control`, { method: "POST", body: JSON.stringify({ surface: "web", client }) });
                      onRefresh();
                    }}
                  >
                    {T("take_control")}
                  </Button>
                </>
              )
            ) : (
              <span className="owner-muted">{T("no_live_session")}</span>
            )}
          </div>
        </div>
        {logTab === "log" && live && (layout === "terminal" || layout === "list") ? (
          <>
            {!owns ? <div className="readonly-note">{T("readonly_note")}</div> : null}
            <XTerm sessionId={live.id} inputEnabled={!!owns} client={client} />
          </>
        ) : null}
        {logTab === "log" && !live ? <div className="empty">{T("start_session_for_log")}</div> : null}
        {logTab === "criteria" && (
          <div>
            {crit.length === 0 ? <div className="empty">{T("no_criteria")}</div> : null}
            {crit.map((c) => (
              <div key={c.index} className={c.state === "pending" ? "crit crit--pending" : "crit"}>
                <span>{c.state === "pass" ? "✓" : c.state === "fail" ? "✗" : "?"}</span>
                <div>
                  <strong>
                    C{c.index} {c.title}
                  </strong>
                  <div className="muted">{c.state === "pass" ? T("proof") : c.state === "fail" ? T("revision") : T("pending")}</div>
                </div>
              </div>
            ))}
          </div>
        )}
        {logTab === "diff" && (
          <div className="empty">
            {T("no_diff")}
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
            {activity.length === 0 ? <div className="empty">{T("no_events")}</div> : null}
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
      <PageHeader title={T("squads")} description={T("squads_sub")} actions={<Button onClick={onCreate}>{T("create_squad")}</Button>} />
      {!skillsRoot ? (
        <Alert tone="warning" title={T("catalog_no_disk_title")}>
          {T("catalog_no_disk_body")}
        </Alert>
      ) : null}
      <SquadGroup title={T("batuta_squads")} hint={T("batuta_squads_hint")} squads={batuta} onEdit={onEdit} onDuplicate={onDuplicate} empty={T("batuta_squads_empty")} />
      <SquadGroup title={T("your_squads")} hint={T("your_squads_hint")} squads={custom} onEdit={onEdit} onDuplicate={onDuplicate} empty={T("your_squads_empty")} />
      <section className="panel">
        <div className="panel-h">
          <span>{T("skills_batuta")}</span>
          <span className="mono muted" style={{ fontSize: 12 }}>
            {skillsRoot || T("no_directory")}
          </span>
        </div>
        <div className="skill-catalog">
          {skills.map((sk) => (
            <div key={sk.id} className="skill-card">
              <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
                <span className="mono" style={{ fontWeight: 600 }}>
                  {sk.id}
                </span>
                <Badge tone={sk.origin === "batuta" ? "accent" : "neutral"}>{sk.origin === "batuta" ? "Batuta" : T("own_skill")}</Badge>
              </div>
              <div style={{ fontSize: 13, color: "var(--text-secondary)" }}>{sk.summary || "—"}</div>
              <div className="path">{sk.installed ? T("installed_host") : T("listed_not_disk")}</div>
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
      {squads.length === 0 ? <EmptyState title={empty} /> : null}
      <div className="cards">
        {squads.map((q) => (
          <Card asChild interactive key={q.id}>
          <button type="button" className="sq-card" onClick={() => onEdit(q)}>
            <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
              <span style={{ fontSize: 16, fontWeight: 600 }}>{q.name || T("unnamed")}</span>
              <span className="inline-actions">
                {q.origin === "batuta" ? <Badge tone="accent">Batuta</Badge> : <Badge tone="neutral">{T("yours")}</Badge>}
                <Badge tone={q.draft ? "warning" : "success"}>{q.draft ? T("draft") : T("valid_v", { v: q.version })}</Badge>
              </span>
            </div>
            <div style={{ fontSize: 13, color: "var(--text-secondary)" }}>{q.purpose || T("no_purpose_yet")}</div>
            <div className="inline-actions">
              {(q.members || []).map((m) => (
                <span key={m.id || m.role} className="role-chip">
                  {m.role || T("role")}
                </span>
              ))}
            </div>
            <div className="path" style={{ display: "flex", justifyContent: "space-between" }}>
              <span>
                v{q.version} · {plural(q.members?.length || 0, "member_one", "member_many")}
              </span>
              <span
                className="linkish"
                onClick={(e) => {
                  e.stopPropagation();
                  onDuplicate(q);
                }}
              >
                {T("duplicate")}
              </span>
            </div>
          </button>
          </Card>
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
      <PageHeader
        eyebrow={T("squads_crumb", { name: name || T("nameless") })}
        title={name || T("squad_editor")}
        actions={
          <Button variant="secondary" onClick={onClose}>
            {T("back")}
          </Button>
        }
      >
        <div className="inline-actions">
          {squad.origin === "batuta" ? <Badge tone="accent">Batuta</Badge> : <Badge tone="neutral">{T("yours")}</Badge>}
          <Badge tone="neutral">{squad.draft ? T("draft_v", { v: squad.version }) : T("version_v", { v: squad.version })}</Badge>
        </div>
      </PageHeader>
      <div className="cards-wide">
        <Field label={T("field_name")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={T("field_purpose")}>
          <Input value={purpose} onChange={(e) => setPurpose(e.target.value)} />
        </Field>
      </div>
      {members.length === 0 ? <Alert tone="danger" title={T("empty_squad_title")}>{T("empty_squad_body")}</Alert> : null}
      {claudePending ? <Alert tone="warning" title={T("iso_unqualified_title")}>{T("iso_unqualified_body")}</Alert> : null}
      {msg ? <Alert tone={tone || "info"} title={msg} /> : null}
      {members.length === 0 ? <div className="panel empty">{T("no_members_yet")}</div> : null}
      <section className="panel editor-panel">
        <div className="table-scroll">
        <table className="editor-table">
          <thead>
            <tr>
              <th>{T("col_role")}</th>
              <th>{T("col_cli")}</th>
              <th>{T("col_model")}</th>
              <th>{T("col_effort")}</th>
              <th>{T("col_account_policy")}</th>
              <th>{T("col_skills")}</th>
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
                      <Badge tone="accent">{T("maestro")}</Badge>
                    ) : (
                      <button type="button" className="linkish" onClick={() => setMembers((ms) => ms.map((m, j) => ({ ...m, maestro: j === i })))}>
                        {T("set_maestro")}
                      </button>
                    )}
                  </td>
                  <td>
                    <Select value={mb.cli} onChange={(e) => patch(i, { cli: e.target.value })}>
                      <option value="codex">Codex</option>
                      <option value="claude">Claude</option>
                      <option value="cursor-agent">Cursor</option>
                      <option value="opencode">OpenCode</option>
                      <option value="gemini">Gemini</option>
                    </Select>
                  </td>
                  <td>
                    <Input value={mb.model || ""} onChange={(e) => patch(i, { model: e.target.value })} placeholder={T("inherit")} />
                  </td>
                  <td>
                    <Select value={mb.effort || "medium"} onChange={(e) => patch(i, { effort: e.target.value })}>
                      <option value="low">{T("effort_low")}</option>
                      <option value="medium">{T("effort_medium")}</option>
                      <option value="high">{T("effort_high")}</option>
                    </Select>
                  </td>
                  <td>
                    <Select value={mb.account_id} onChange={(e) => patch(i, { account_id: e.target.value })}>
                      <option value="">{T("no_account")}</option>
                      {accounts.map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.label}
                        </option>
                      ))}
                    </Select>
                    <div className="muted" style={{ fontSize: 11.5, marginTop: 4 }}>
                      {acc ? T("fixed_account_iso", { iso: iso?.text || "" }) : T("no_account")}
                    </div>
                  </td>
                  <td>
                    <SkillPicker value={mb.skills || []} catalog={skills} onChange={(next) => patch(i, { skills: next })} />
                  </td>
                  <td>
                    <IconButton label={T("remove")} onClick={() => setMembers((ms) => ms.filter((_, j) => j !== i))}>
                      ×
                    </IconButton>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        </div>
      </section>
      <div className="inline-actions">
        <Button
          variant="soft"
          onClick={() =>
            setMembers((ms) => [...ms, { role: "", cli: "codex", account_id: accounts[0]?.id || "", maestro: ms.length === 0, effort: "medium" }])
          }
        >
          {T("add_member")}
        </Button>
        <span className="muted">{plural(members.length, "member_one", "member_many")}</span>
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
          {T("validate")}
        </Button>
        {workspaceId ? (
          <Button
            variant="secondary"
            onClick={async () => {
              const saved = await api<Squad>("/v0/squads", { method: "POST", body: JSON.stringify(draft) });
              await api("/v0/missions", { method: "POST", body: JSON.stringify({ workspace_id: workspaceId, title: name || T("new_mission_title"), squad_id: saved.id }) });
              onSaved();
            }}
          >
            {T("create_mission_squad")}
          </Button>
        ) : null}
        <Button
          onClick={async () => {
            await api<Squad>("/v0/squads", { method: "POST", body: JSON.stringify(draft) });
            onSaved();
          }}
        >
          {T("save_version")}
        </Button>
      </div>
      <div className="info-cards">
        <div className="panel" style={{ padding: "var(--cardpad)" }}>
          <h2 style={{ margin: "0 0 8px", fontSize: 16 }}>{T("concurrency")}</h2>
          <p className="sub">{T("concurrency_body")}</p>
        </div>
        <div className="panel" style={{ padding: "var(--cardpad)" }}>
          <h2 style={{ margin: "0 0 8px", fontSize: 16 }}>{T("precedence")}</h2>
          <p className="sub">{T("precedence_body")}</p>
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
  const [edit, setEdit] = useState<Account | null>(null);
  return (
    <div className="page">
      <PageHeader title={T("nav_accounts")} description={T("accounts_sub")} actions={<Button onClick={onOpen}>{T("add_account")}</Button>} />
      <div className="stats">
        <div className="stat-card">
          <Stat label={T("stat_accounts")} value={accounts.length} />
        </div>
        <div className="stat-card">
          <Stat label={T("stat_sessions")} value={`${live.length}${cap ? ` / ${cap}` : ""}`} />
        </div>
        <div className="stat-card">
          <Stat label={T("stat_limits")} value={newest ? relTime(newest) : T("never")} />
        </div>
        <div className="stat-card">
          <Stat label={T("stat_pending")} value={pending} />
        </div>
      </div>
      <section className="panel">
        <div className="acct-grid caps" style={{ cursor: "default" }}>
          <span>{T("accounts")}</span>
          <span>{T("col_provider")}</span>
          <span>{T("col_isolation")}</span>
          <span>{T("col_sessions")}</span>
          <span>{T("col_known_limit")}</span>
          <span>{T("col_source")}</span>
        </div>
        {accounts.map((a) => {
          const lim = limits.find((l) => l.account_id === a.id);
          const iso = isolationLabel(a.isolation);
          const used = live.filter((s) => s.account_id === a.id).length;
          return (
            <button key={a.id} type="button" className="acct-grid" onClick={() => setEdit(a)}>
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
                <div className="muted">{lim?.percent == null ? T("unknown") : T("limit_pct", { pct: Math.round(lim.percent), window: lim.window || T("window") })}</div>
                <Progress value={lim?.percent} unknown={lim?.percent == null} />
              </span>
              <span className="mono muted">{lim?.origin || lim?.source || "—"}</span>
            </button>
          );
        })}
        {accounts.length === 0 ? <EmptyState title={T("no_accounts_yet")} /> : null}
      </section>
      <p className="muted">{T("limits_footnote")}</p>
      <AddAccount open={open} onClose={onClose} onSaved={onSaved} />
      <RenameAccount account={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); onSaved(); }} />
    </div>
  );
}

function RenameAccount({
  account,
  onClose,
  onSaved,
}: {
  account: Account | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [label, setLabel] = useState("");
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");
  useEffect(() => {
    setLabel(account?.label || "");
    setErr("");
  }, [account]);
  return (
    <Dialog
      open={!!account}
      title={T("rename_account")}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {T("cancel")}
          </Button>
          <Button
            disabled={!label.trim() || !account || saving}
            loading={saving}
            onClick={async () => {
              if (!account) return;
              setSaving(true);
              setErr("");
              try {
                await api<Account>(`/v0/accounts/${account.id}`, { method: "PATCH", body: JSON.stringify({ label }) });
                onSaved();
              } catch (e) {
                setErr(e instanceof Error ? e.message : String(e));
              } finally {
                setSaving(false);
              }
            }}
          >
            {T("save")}
          </Button>
        </>
      }
    >
      <Field label={T("local_label")} hint={T("label_hint", { provider: account?.provider || "", adapter: account?.adapter || "" })}>
        <Input value={label} onChange={(e) => setLabel(e.target.value)} />
      </Field>
      {err ? <p className="err">{err}</p> : null}
    </Dialog>
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
    { id: "codex", title: T("provider_codex"), hint: T("provider_codex_hint") },
    { id: "claude", title: T("provider_claude"), hint: T("provider_claude_hint") },
    { id: "cursor-agent", title: T("provider_cursor"), hint: T("provider_cursor_hint") },
    { id: "opencode", title: T("provider_opencode"), hint: T("provider_opencode_hint") },
    { id: "gemini", title: T("provider_gemini"), hint: T("provider_gemini_hint") },
  ];
  return (
    <Drawer
      open={open}
      title={T("add_account")}
      onClose={onClose}
      footer={
        step === 1 ? (
          <>
            <Button variant="secondary" onClick={onClose}>
              {T("cancel")}
            </Button>
            <Button disabled={!label} onClick={() => setStep(2)}>
              {T("continue")}
            </Button>
          </>
        ) : step === 2 ? (
          <>
            <Button variant="secondary" onClick={() => setStep(1)}>
              {T("back")}
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
              {T("connect_official")}
            </Button>
          </>
        ) : (
          <Button onClick={onSaved}>{T("save_account")}</Button>
        )
      }
    >
      <Stepper steps={[T("step_provider"), T("step_connect"), T("step_confirm")]} current={step} />
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
          <Field label={T("account_name")} hint={T("account_name_hint")}>
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={T("account_placeholder")} />
          </Field>
          <Badge tone={iso.tone}>{iso.text}</Badge>
        </div>
      )}
      {step === 2 && (
        <div style={{ marginTop: 16, display: "flex", flexDirection: "column", gap: 12 }}>
          <p>
            {label} · {adapter}
          </p>
          <p className="muted">{T("connect_body")}</p>
        </div>
      )}
      {step === 3 && draft && (
        <div style={{ marginTop: 16 }}>
          <Alert tone="success" title={T("connected")}>
            {T("isolation_login", { iso: draft.isolation })}
          </Alert>
          <dl className="detail" style={{ border: 0, padding: "12px 0" }}>
            <dt>{T("col_account")}</dt>
            <dd>{draft.label}</dd>
            <dt>{T("identity")}</dt>
            <dd className="mono">{draft.vault_ref ? `${draft.vault_ref.slice(0, 18)}…` : "—"}</dd>
            <dt>{T("col_isolation")}</dt>
            <dd>{draft.isolation}</dd>
            <dt>{T("col_sessions")}</dt>
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
      <PageHeader title={T("activity")} description={T("activity_sub", { cursor })} />
      <Tabs
        active={filter}
        onChange={setFilter}
        items={[
          { id: "all", label: T("filter_everything") },
          { id: "sessions", label: T("filter_sessions") },
          { id: "deliveries", label: T("filter_deliveries") },
          { id: "limits", label: T("filter_limits") },
          { id: "clients", label: T("filter_clients") },
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
                  {mission ? <div className="mono muted">{T("mission_colon", { title: mission.title })}</div> : null}
                </span>
              </button>
            );
          })}
          {filtered.length === 0 ? <div className="empty">{T("no_events_filter")}</div> : null}
        </section>
        <section className="panel">
          <div className="panel-h">{T("sessions")}</div>
          <p className="empty" style={{ paddingBottom: 0 }}>
            {T("disconnect_note")}
          </p>
          <div className="acct-grid caps" style={{ gridTemplateColumns: "1.2fr 1fr 1fr 1fr" }}>
            <span>{T("col_session")}</span>
            <span>{T("col_process")}</span>
            <span>{T("col_client")}</span>
            <span>{T("col_work")}</span>
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
      <PageHeader
        eyebrow={`${mission?.title || T("mission")} / ${T("delivery_state")}`}
        title={watch?.delivery || mission?.title || T("review")}
        description={T("review_sub")}
        actions={
          <div className="inline-actions">
            <Button variant="secondary" onClick={onBack}>
              {T("back_to_room")}
            </Button>
            <Button variant="secondary" disabled title={T("reopen_title")}>
              {T("reopen_task")}
            </Button>
          </div>
        }
      >
        <Badge tone={watch?.available ? "success" : "neutral"} dot>
          {watch?.available ? T("with_journal") : T("without_journal")}
        </Badge>
      </PageHeader>
      <div className="cards-wide">
        <section className="panel">
          <div className="panel-h">Diff</div>
          <div className="empty">{T("no_review_files")}</div>
        </section>
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--gap)" }}>
          <section className="panel">
            <div className="panel-h">{T("criteria")}</div>
            {crit.length === 0 ? <div className="empty">{T("no_criteria_journal")}</div> : null}
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
            <div className="panel-h">{T("evidence")}</div>
            {ev.length === 0 ? <div className="empty">{T("no_evidence")}</div> : null}
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
  const [picking, setPicking] = useState(false);
  const [pickErr, setPickErr] = useState("");
  const slug = slugify(name);

  async function pickFolder() {
    setPicking(true);
    setPickErr("");
    try {
      const got = await api<{ path?: string; canceled?: boolean }>("/v0/workspaces/pick-root", { method: "POST" });
      if (got?.canceled || !got?.path) return;
      setRoot(got.path);
      if (!name.trim()) {
        const base = got.path.replace(/[\\/]+$/, "").split(/[\\/]/).filter(Boolean).pop() || "";
        if (base) setName(base);
      }
    } catch (e) {
      setPickErr(e instanceof Error ? e.message : String(e));
    } finally {
      setPicking(false);
    }
  }

  return (
    <Dialog
      open={open}
      title={T("new_workspace")}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {T("cancel")}
          </Button>
          <Button
            disabled={!name || !root}
            onClick={async () => {
              const w = await api<Workspace>("/v0/workspaces", { method: "POST", body: JSON.stringify({ name, root }) });
              setName("");
              setRoot("");
              setPickErr("");
              onCreated(w);
            }}
          >
            {T("create")}
          </Button>
        </>
      }
    >
      <div className="lyra-wscreate">
        <Field label={T("field_name")}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="acme" />
        </Field>
        <div>
          <span className="lyra-label">{T("field_path")}</span>
          <div className="path-pick">
            <div className="lyra-wscreate__slug">
              <input
                className="lyra-wscreate__slug-input"
                value={root}
                onChange={(e) => setRoot(e.target.value)}
                placeholder={slug || T("path_placeholder")}
              />
            </div>
            <Button variant="secondary" type="button" loading={picking} disabled={picking} onClick={pickFolder} iconLeft={<FolderOpen size={16} />}>
              {T("pick_folder")}
            </Button>
          </div>
          {pickErr ? <p className="lyra-hint lyra-hint--error">{pickErr}</p> : <p className="lyra-hint">{T("path_hint")}</p>}
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
        <button key={id} type="button" className="skill-chip skill-chip--on" onClick={() => toggle(id)} title={T("remove_skill")}>
          {id} ×
        </button>
      ))}
      <details className="skill-add">
        <summary>{T("add_skill")}</summary>
        <div className="skill-add__pop">
          <div className="skill-add__list">
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
            {catalog.some((sk) => sk.origin !== "batuta") ? <div className="skill-add__label">{T("on_disk")}</div> : null}
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
          </div>
          <input
            className="skill-custom"
            value={extra}
            placeholder={T("custom_skill_name")}
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
