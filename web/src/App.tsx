import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTheme } from "@lyra-ds/react/theme-provider";
import { Activity, FolderOpen, House, KeyRound, Moon, Sun, Target, Users } from "lucide-react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  api,
  executionLabel,
  type ExecutionState,
  attentionLine,
  clock,
  criteria,
  evidence,
  eventOrigin,
  gateMark,
  gateStages,
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
  type SetupConfig,
  type SetupDraft,
  type Skill,
  type Squad,
  type Watch,
  type Workspace,
  plural,
} from "./lib";
import { bounded, followEvents, READ_INTERVAL } from "./sync";
import { T } from "./i18n";
import { MissionPlanning } from "./MissionPlanning";
import { MissionJourney } from "./MissionJourney";
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
  const [eventError, setEventError] = useState("");
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [missions, setMissions] = useState<Mission[]>([]);
  const [squads, setSquads] = useState<Squad[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [activity, setActivity] = useState<Event[]>([]);
  const [limits, setLimits] = useState<Limit[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [missionState, setMissionState] = useState<ExecutionState | null>(null);
  const [journeyRefresh, setJourneyRefresh] = useState(0);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [skillsRoot, setSkillsRoot] = useState("");
  const [editing, setEditing] = useState<Squad | null>(null);
  const [addAccount, setAddAccount] = useState(false);
  const [activeMission, setActiveMission] = useState<Mission | null>(null);
  const [wsId, setWsId] = useState(() => localStorage.getItem("bt-ws") || "");
  const [wsDialog, setWsDialog] = useState(false);
  const [loginAccount, setLoginAccount] = useState<Account | null>(null);
  const [loginSession, setLoginSession] = useState<Session | null>(null);
  const [setupWs, setSetupWs] = useState<Workspace | null>(null);
  const [setupDrafts, setSetupDrafts] = useState<Record<string, SetupDraft>>({});

  const currentWs = workspaces.find((w) => w.id === wsId) || workspaces[0];
  useEffect(() => {
    if (currentWs && currentWs.id !== wsId) {
      setWsId(currentWs.id);
      localStorage.setItem("bt-ws", currentWs.id);
    }
  }, [currentWs, wsId]);

  const refreshGeneration = useRef(0);
  const refreshController = useRef<AbortController | null>(null);

  async function refresh() {
    if (refreshController.current) return;
    const controller = new AbortController();
    refreshController.current = controller;
    const generation = ++refreshGeneration.current;
    try {
      await bounded(async signal => {
        const [ws, msn, sq, ac, ev, sess, lim, sk] = await Promise.all([
          api<Workspace[]>("/v0/workspaces", { signal }),
          api<Mission[]>("/v0/missions", { signal }),
          api<Squad[]>("/v0/squads", { signal }),
          api<Account[]>("/v0/accounts", { signal }),
          api<Event[]>("/v0/activity", { signal }),
          api<Session[]>("/v0/sessions", { signal }),
          api<Limit[]>("/v0/limits", { signal }),
          api<{ root?: string; skills?: Skill[] }>("/v0/skills", { signal }),
        ]);
        if (generation !== refreshGeneration.current || signal.aborted) return;
        if (![ws, msn, sq, ac, ev, sess, lim].every(value => value == null || (Array.isArray(value) && value.every(item => item && typeof item === "object" && !Array.isArray(item)))) ||
            (ws || []).some(w => !w.id || typeof w.id !== "string" || typeof w.name !== "string" || typeof w.root !== "string") ||
            (msn || []).some(m => !m.id || typeof m.id !== "string" || !m.workspace_id || typeof m.workspace_id !== "string" || typeof m.title !== "string" || typeof m.status !== "string") ||
            (sk?.skills != null && !Array.isArray(sk.skills))) throw new Error(T("journey_invalid_state"));
        setWorkspaces(ws || []);
        setMissions(msn || []);
        setActiveMission(selected => selected ? (msn || []).find(m => m.id === selected.id && m.workspace_id === selected.workspace_id) || null : null);
        setSquads(sq || []);
        setAccounts(ac || []);
        setActivity(ev || []);
        setSessions(sess || []);
        setLimits(lim || []);
        setSkills(sk?.skills || []);
        setSkillsRoot(sk?.root || "");
        setJourneyRefresh(n => n + 1);
        setErr("");
      }, controller.signal);
    } catch (e) {
      if (generation === refreshGeneration.current) setErr(e instanceof Error ? e.message : String(e));
    } finally {
      controller.abort();
      if (refreshController.current === controller) refreshController.current = null;
    }
  }

  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    void refreshRef.current();
    const timer = window.setInterval(() => { void refreshRef.current(); }, READ_INTERVAL);
    const stopEvents = followEvents(() => { void refreshRef.current(); }, setEventError);
    return () => {
      ++refreshGeneration.current;
      refreshController.current?.abort();
      refreshController.current = null;
      window.clearInterval(timer);
      stopEvents();
    };
  }, []);

  const missionWatch = missionState?.watch || null;
  const selectedState = activeMission && missionState?.target.mission_id === activeMission.id && missionState.target.binding.workspace_id === activeMission.workspace_id ? missionState : null;
  const watch = activeMission && activeMissionMatch(activeMission, missionWatch) ? missionWatch : null;
  const attention = useMemo(() => attentionLine(watch), [watch]);
  const wsMissions = missions.filter((m) => !currentWs || m.workspace_id === currentWs.id);
  const attentionCount = attention ? 1 : 0;

  function go(id: Screen) {
    if (id === "room") setJourneyRefresh(n => n + 1);
    setScreen(id);
    setAddAccount(false);
  }

  function openSetup(workspace: Workspace) {
    setSetupWs(workspace);
    go("setup");
  }

  function pickWorkspace(id: string) {
    setActiveMission(null);
    setMissionState(null);
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
      width={320}
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
      <Shell scroll="content" className="bt-shell" sidebar={!phone ? sidebar : undefined} topbar={topbar} sidebarLabel="Batuta Console">
        {err ? <div className="err">{err}</div> : null}
        {eventError ? <div className="err">{eventError}</div> : null}
        {(screen === "home" || screen === "missions") && currentWs ? (
          <div className="inline-actions" style={{ padding: "var(--cardpad)" }}>
            <Button variant="secondary" onClick={() => openSetup(currentWs)}>
              {T((currentWs.setup?.ready ?? currentWs.profile) ? "setup_details" : "setup_title")}
            </Button>
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
            squads={squads.filter((q) => squadReady(q, accounts))}
            workspace={currentWs}
            onSetup={() => currentWs && openSetup(currentWs)}
            watch={watch}
            selectedState={selectedState}
            attention={attention}
            onOpen={(m) => {
              setActiveMission(m);
              go("room");
            }}
            onCreated={(m) => {
              setActiveMission(m);
              go("room");
              refresh();
            }}
          />
        )}
        <MissionJourney mission={activeMission} refreshVersion={journeyRefresh} onWatch={(_watch, state) => setMissionState(state || null)}>
        {journey => screen === "room" ? (
          <Room
            key={activeMission ? `${activeMission.workspace_id}:${activeMission.id}` : "none"}
            journey={journey}
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
        ) : null}
        </MissionJourney>
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
            onLogin={async (a) => {
              const sess = await api<Session>(`/v0/accounts/${a.id}/login`, {
                method: "POST",
                body: JSON.stringify({ owner: "web", client: webClient }),
              });
              setLoginAccount(a);
              setLoginSession(sess);
              go("login");
            }}
          />
        )}
        {screen === "login" && loginSession && (
          <LoginScreen
            account={loginAccount}
            sessionId={loginSession.id}
            client={webClient}
            onBack={() => {
              refresh();
              go("accounts");
            }}
          />
        )}
        {screen === "setup" && setupWs && (
          <SetupScreen
            key={setupWs.id}
            workspace={workspaces.find((w) => w.id === setupWs.id) || setupWs}
            accounts={accounts}
            draft={setupDrafts[setupWs.id] || emptySetupDraft}
            onDraft={(draft) => setSetupDrafts((previous) => ({ ...previous, [setupWs.id]: draft }))}
            client={webClient}
            onRefresh={refresh}
            onStart={async (draft) => {
              const sess = await api<Session>(`/v0/workspaces/${setupWs.id}/init`, {
                method: "POST",
                body: JSON.stringify({ ...draft, owner: "web", client: webClient }),
              });
              ++refreshGeneration.current;
              setWorkspaces((previous) => previous.map((w) => {
                if (w.id !== setupWs.id) return w;
                const old = w.setup?.attempt?.session_id === sess.id ? w.setup.attempt : undefined;
                return { ...w, setup: { ...w.setup, ready: w.setup?.ready ?? Boolean(w.profile), state: w.setup?.state || "partial", attempt: {
                  session_id: sess.id, account_id: sess.account_id || draft.account_id,
                  state: old?.state || (sess.process === "running" ? "running" : sess.process === "unknown" ? "interrupted" : "exited"),
                  process: sess.process, stop_requested: old?.stop_requested || false,
                  recovery_reason: sess.recovery_reason, created_at: sess.created_at || "", ended_at: sess.ended_at,
                } } };
              }));
              await refresh();
            }}
            onCancel={async (sessionId) => {
              await api(`/v0/sessions/${sessionId}/stop`, { method: "POST" });
              ++refreshGeneration.current;
              setWorkspaces((previous) => previous.map((w) => w.id === setupWs.id && w.setup?.attempt?.session_id === sessionId
                ? { ...w, setup: { ...w.setup, attempt: { ...w.setup.attempt, stop_requested: true } } } : w));
              await refresh();
            }}
            onBack={() => {
              refresh();
              go("home");
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
          setWorkspaces((previous) => [...previous.filter((item) => item.id !== w.id), w]);
          pickWorkspace(w.id);
          refresh();
          if (!(w.setup?.ready ?? w.profile)) openSetup(w);
        }}
      />
    </div>
  );
}

function chromeScreen(screen: Screen): Screen {
  if (screen === "editor") return "squads";
  if (screen === "room" || screen === "review") return "missions";
  if (screen === "login") return "accounts";
  if (screen === "setup") return "home";
  return screen;
}

function squadReady(q: Squad, accounts: Account[]) {
  const members = q.members || [];
  const maestros = members.filter((m) => m.maestro);
  if (maestros.length !== 1 || !(maestros[0].skills || []).length) return false;
  return members.every((m) => {
    const account = accounts.find((a) => a.id === m.account_id);
    return Boolean(m.role && m.cli && account && account.connected && account.authed && account.adapter === m.cli);
  });
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
  selectedState,
  attention,
  onOpen,
  onCreated,
  onSetup,
}: {
  missions: Mission[];
  squads: Squad[];
  workspace?: Workspace;
  watch: Watch | null;
  selectedState: ExecutionState | null;
  attention: string;
  onOpen: (m: Mission) => void;
  onCreated: (m: Mission) => void;
  onSetup: () => void;
}) {
  const [filter, setFilter] = useState("all");
  const [creating, setCreating] = useState(false);
  const stateFor = (m: Mission) => selectedState?.target.mission_id === m.id && selectedState.target.binding.workspace_id === m.workspace_id ? selectedState : null;
  const completed = (m: Mission) => {
    const state = stateFor(m);
    return !!state?.opened_digest && !state.active && !state.watch?.pending_question && state.watch?.header?.state === "done" && !!state.gate && (!state.gate.required || state.gate.cleared);
  };
  const active = missions.filter(m => !completed(m));
  const done = missions.filter(completed);
  const shown = filter === "active" ? active : filter === "done" ? done : missions;
  return (
    <div className="page">
      <PageHeader
        title={T("missions")}
        actions={
          <Button
            onClick={() => {
              if (!workspace) return alert(T("need_ws_squad"));
              if (!(workspace.setup?.ready ?? workspace.profile)) return onSetup();
              if (squads.length === 0) return alert(T("need_ready_squad"));
              setCreating(true);
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
          const state = stateFor(m);
          const status = completed(m) ? "done" : state ? "running" : /done|complete|conclu/i.test(m.status) ? "unknown" : m.status;
          const exact = activeMissionMatch(m, watch);
          const crit = criteria(exact ? watch : null);
          const pct = crit.length ? Math.round((100 * crit.filter(c => c.state === "pass").length) / crit.length) : null;
          const attn = exact ? attention : m.attention;
          return (
            <button key={m.id} type="button" className={attn ? "mission-grid row-warn" : "mission-grid"} onClick={() => onOpen(m)}>
              <span style={{ display: "flex", flexDirection: "column" }}>
                <span style={{ fontWeight: 500 }}>{m.title}</span>
                {m.brief ? <span className="muted" style={{ fontSize: 12 }}>{m.brief}</span> : null}
                <span className="mono muted" style={{ fontSize: 12 }}>
                  {m.id}
                </span>
              </span>
              <span style={{ fontSize: 13 }}>{m.squad_snapshot?.name || "—"}</span>
              <span>
                <Badge tone={missionTone(status, attn ? "1" : m.attention)} dot>
                  {state ? executionLabel(state, true, false) : missionStatusLabel(status, attn ? "1" : m.attention)}
                </Badge>
              </span>
              <span style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                <span className="mono" style={{ fontSize: 12 }}>
                  {pct == null ? T("no_journal") : T("criteria_pct", { pct })}
                </span>
                <Progress value={pct} unknown={pct == null} />
              </span>
              <span style={{ fontSize: 13, color: attn ? "var(--warning-text)" : undefined, fontWeight: attn ? 600 : 400 }}>
                {attn || T("follow_gates")}
              </span>
            </button>
          );
        })}
      </section>
      <NewMissionDialog
        open={creating}
        workspace={workspace}
        squads={squads}
        onClose={() => setCreating(false)}
        onCreated={(m) => {
          setCreating(false);
          onCreated(m);
        }}
      />
    </div>
  );
}

function NewMissionDialog({
  open,
  workspace,
  squads,
  onClose,
  onCreated,
}: {
  open: boolean;
  workspace?: Workspace;
  squads: Squad[];
  onClose: () => void;
  onCreated: (m: Mission) => void;
}) {
  const [objective, setObjective] = useState("");
  const [squadID, setSquadID] = useState(squads[0]?.id || "");
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [error, setError] = useState("");
  const [matches, setMatches] = useState<Mission[]>([]);
  const lock = useRef(false);
  useEffect(() => {
    if (open && !squads.some((q) => q.id === squadID)) setSquadID(squads[0]?.id || "");
  }, [open, squads, squadID]);

  return (
    <Dialog
      open={open}
      title={T("new_mission")}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {T("cancel")}
          </Button>
          <Button
            disabled={busy || blocked || !objective.trim() || !squadID || !workspace}
            onClick={async () => {
              if (!workspace || lock.current || blocked) return;
              lock.current = true;
              const text = objective.trim();
              const title = text.split("\n")[0].slice(0, 80);
              setBusy(true);
              setBlocked(true);
              setError("");
              try {
                const m = await api<Mission>("/v0/missions", {
                  method: "POST",
                  body: JSON.stringify({ workspace_id: workspace.id, title, brief: text, squad_id: squadID }),
                });
                if (!m?.id || m.workspace_id !== workspace.id || m.squad_id !== squadID || m.brief !== text) throw new Error(T("journey_invalid_state"));
                setBlocked(false);
                setObjective("");
                onCreated(m);
              } catch (e) {
                setError(`${T("journey_inspect_first")} ${e instanceof Error ? e.message : String(e)}`);
              } finally {
                lock.current = false;
                setBusy(false);
              }
            }}
          >
            {T("create")}
          </Button>
        </>
      }
    >
      {error ? <Alert tone="warning">{error}</Alert> : null}
      {blocked ? <Button variant="secondary" disabled={busy} onClick={async () => {
        if (!workspace || lock.current) return;
        lock.current = true;
        setBusy(true);
        try {
          const catalog = await api<Mission[]>("/v0/missions");
          const found = catalog.filter(m => m.workspace_id === workspace.id && m.squad_id === squadID && m.brief === objective.trim());
          setMatches(found);
          setBlocked(found.length > 0);
          setError(found.length ? T("journey_created_matches") : T("journey_no_matches"));
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e));
        } finally {
          lock.current = false;
          setBusy(false);
        }
      }}>{T("journey_inspect_created")}</Button> : null}
      {matches.map(m => <Button key={m.id} variant="secondary" onClick={() => onCreated(m)}>{m.title} · {m.id}</Button>)}
      <Field label={T("mission_objective")} hint={T("mission_objective_hint")}>
        <textarea className="mission-brief" rows={5} value={objective} onChange={(e) => setObjective(e.target.value)} />
      </Field>
      <Field label={T("mission_squad")}>
        <Select value={squadID} onChange={(e) => setSquadID(e.target.value)}>
          {squads.map((q) => (
            <option key={q.id} value={q.id}>
              {q.name}
            </option>
          ))}
        </Select>
      </Field>
    </Dialog>
  );
}

function activeMissionMatch(m: Mission, watch: Watch | null) {
  return Boolean(watch?.available && watch.source === "current" && watch.scope === "mission" && watch.mission_id === m.id && watch.workspace_id === m.workspace_id && watch.delivery && (!m.binding?.delivery_id || watch.delivery === m.binding.delivery_id));
}

function Room({
  mission,
  journey,
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
  journey: ReactNode;
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
  const live = sessions.find((s) => s.process === "running");
  const shown = live || sessions[0];
  const owns = live?.owner === "web" && live.owner_client === client;
  const members = mission?.squad_snapshot?.members || [];
  const attn = attentionLine(watch);
  const rows = taskRows(watch);
  const tasks = rows.length
    ? rows.map((r, i) => ({
        key: r.task || String(i),
        name: r.title,
        role: members[i]?.role || T("agent_default"),
        memberId: members[i]?.id || "",
        state: r.state,
        account: accounts.find((a) => a.id === members[i]?.account_id)?.label || members[i]?.account_id || "—",
        cli: members[i]?.cli || "",
        model: members[i]?.model || T("inherit"),
        task: r.task,
        gates: r.gates,
        commit: r.commit,
      }))
    : members.map((mb, i) => ({
        key: mb.id || String(i),
        name: mb.role || T("member_default"),
        role: mb.role || T("agent_default"),
        memberId: mb.id,
        state: "",
        account: accounts.find((a) => a.id === mb.account_id)?.label || mb.account_id || "—",
        cli: mb.cli,
        model: mb.model || T("inherit"),
        task: "",
        gates: ["pending", "pending", "pending", "pending"],
        commit: "",
      }));
  const selected = tasks[sel] || tasks[0];
  const stageMarks = (selected?.gates || ["pending", "pending", "pending", "pending"]).map((state) => gateMark(state));
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
              <span key={g} className={stageMarks[i] === "✓" ? "gate gate--done" : stageMarks[i] === "✗" ? "gate gate--fail" : "gate gate--wait"} title={T(gateStages[i])}>
                {g} {stageMarks[i]}
              </span>
            ))}
            <Button variant="secondary" onClick={onReview}>
              {T("review_delivery")}
            </Button>
            {mission && live ? (
              <Button
                variant="secondary"
                onClick={async () => {
                  await api(`/v0/sessions/${live.id}/stop`, { method: "POST" });
                  onRefresh();
                }}
              >
                {T("stop_session")}
              </Button>
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
      <section className="panel plan-card">
        <div className="panel-h">{T("plan_flow")}</div>
        <div className="plan-body">
          <div className="caps">{T("mission_objective")}</div>
          <p className="mission-objective">{mission?.brief || mission?.title || "—"}</p>
          {mission ? <MissionPlanning mission={mission} sessions={sessions} client={client} onRefresh={onRefresh} /> : null}
          {journey}
        </div>
      </section>
      {!skillsRoot && !live ? (
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
        <div className="room-split">
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
            <div className="caps">{T("stages")}</div>
            <div className="gate-list">
              {gateStages.map((key, i) => {
                const state = selected?.gates[i] || "pending";
                return (
                  <div key={key} className="gate-row">
                    <span className={state === "pass" ? "gate gate--done" : state === "fail" ? "gate gate--fail" : "gate gate--wait"}>G{i}</span>
                    <span>{T(key)}</span>
                    <span className="muted">{state === "silent" ? T("gate_silent") : state === "pass" ? T("proof") : state === "fail" ? T("revision") : T("pending")}</span>
                  </div>
                );
              })}
            </div>
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
            {selected?.commit ? T("journal_commit", { sha: selected.commit.slice(0, 12) }) : T("no_diff")}
          </div>
        )}
        {logTab === "history" && (
          <div>
            {(watch?.log_lines || []).map((line, i) => (
              <div key={`log-${i}`} className="hist-grid">
                <span className="mono muted">{i + 1}</span>
                <span style={{ fontWeight: 500 }}>{watch?.log_title || T("journal")}</span>
                <span>{line}</span>
              </div>
            ))}
            {activity
              .filter((e) => !mission || e.payload?.mission_id === mission.id || e.kind.startsWith("session"))
              .slice(0, 12)
              .map((e) => {
                const origin = eventOrigin(e.kind);
                return (
                  <div key={e.seq} className="hist-grid">
                    <span className="mono muted">{clock(e.at)}</span>
                    <span style={{ fontWeight: 500 }}>{origin.label}</span>
                    <span>{e.kind}</span>
                  </div>
                );
              })}
            {(watch?.log_lines || []).length === 0 && activity.length === 0 ? <div className="empty">{T("no_journal_lines")}</div> : null}
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

function LoginScreen({
  account,
  sessionId,
  client,
  onBack,
}: {
  account: Account | null;
  sessionId: string;
  client: string;
  onBack: () => void;
}) {
  return (
    <div className="page">
      <PageHeader
        title={T("login_title", { label: account?.label || account?.adapter || "" })}
        description={T("login_hint")}
        actions={
          <Button variant="secondary" onClick={onBack}>
            {T("back_accounts")}
          </Button>
        }
      />
      <section className="log-panel log-panel--fill">
        <XTerm sessionId={sessionId} inputEnabled client={client} />
      </section>
    </div>
  );
}

const emptySetupDraft: SetupDraft = {
  config: { stack: "", methodology: "", test: "", build: "", install: "", execution: "", worktree: "", template: "" },
  account_id: "",
  brief: "",
};

function validSetupText(value: string, limit: number, multiline = false) {
  return (multiline || Boolean(value.trim())) && new TextEncoder().encode(value).length <= limit &&
    !(multiline ? /\0/ : /[\0\r\n]/).test(value);
}

function setupAccountReady(account?: Account) {
  const r = account?.readiness;
  return Boolean(account && (account.adapter === "codex" || account.adapter === "claude") &&
    account.connected && r?.ready && r.supported &&
    r.available && r.cli_compatible && r.connected && r.credential_configured && r.authenticated);
}

function attemptLabel(state: string) {
  if (state === "running") return T("setup_running");
  if (state === "interrupted") return T("setup_interrupted");
  if (state === "cancel_requested") return T("setup_cancel_requested");
  if (state === "exited_incomplete") return T("setup_incomplete");
  if (state === "exited") return T("setup_exited");
  return state;
}

function SetupScreen({
  workspace,
  accounts,
  draft,
  onDraft,
  client,
  onStart,
  onRefresh,
  onCancel,
  onBack,
}: {
  workspace: Workspace;
  accounts: Account[];
  draft: SetupDraft;
  onDraft: (draft: SetupDraft) => void;
  client: string;
  onStart: (draft: SetupDraft) => Promise<void>;
  onCancel: (sessionId: string) => Promise<void>;
  onRefresh: () => Promise<void>;
  onBack: () => void;
}) {
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [observing, setObserving] = useState("");
  const ready = workspace.setup?.ready ?? workspace.profile;
  const attempt = workspace.setup?.attempt;
  const live = attempt?.process === "running" && attempt.state === "running";
  const selected = accounts.find((a) => a.id === draft.account_id);
  const accountReady = setupAccountReady(selected);
  const configValid = Object.values(draft.config).every((value) => validSetupText(value, 8192)) &&
    ["sequential", "parallel"].includes(draft.config.execution) && ["off", "medium+", "always"].includes(draft.config.worktree);
  const notesValid = validSetupText(draft.brief, 65536, true);

  function edit(next: SetupDraft) {
    setConfirmed(false);
    onDraft(next);
  }

  function field(key: keyof SetupConfig, label: string) {
    return <Field key={key} label={label}>
      <Input required value={draft.config[key]} aria-invalid={Boolean(draft.config[key]) && !validSetupText(draft.config[key], 8192)}
        onChange={(e) => edit({ ...draft, config: { ...draft.config, [key]: e.target.value } })} />
    </Field>;
  }

  async function act(action: () => Promise<void>) {
    setBusy(true);
    setErr("");
    try {
      await action();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      setConfirmed(false);
    }
  }

  return (
    <div className="page">
      <PageHeader title={T("setup_title")} description={`${workspace.name} · ${workspace.root}`} actions={<Button variant="secondary" onClick={onBack}>{T("back_home")}</Button>} />
      <p className="muted">{T("setup_hint")}</p>
      <section className="panel plan-body" aria-label={T("setup_configuration")}>
        <h2>{T(ready ? "setup_ready" : "setup_not_ready")}</h2>
        <p>{workspace.setup?.configuration_code}</p>
        <p>{workspace.setup?.code}</p>
        <Button variant="secondary" disabled={busy} onClick={() => act(onRefresh)}>{T("setup_refresh")}</Button>
      </section>
      {attempt ? <section className="panel plan-body" aria-label={T("setup_attempt")}>
        <h2>{T("setup_attempt")}</h2>
        <dl>
          <dt>{T("setup_session")}</dt><dd>{attempt.session_id}</dd>
          <dt>{T("setup_account")}</dt><dd>{attempt.account_id}</dd>
          <dt>{T("col_status")}</dt><dd>{attemptLabel(attempt.state)}</dd>
          <dt>{T("setup_process")}</dt><dd>{processLife(attempt.process)}</dd>
          <dt>{T("setup_created")}</dt><dd>{attempt.created_at}</dd>
          {attempt.ended_at ? <><dt>{T("setup_ended")}</dt><dd>{attempt.ended_at}</dd></> : null}
        </dl>
        {attempt.recovery_reason ? <p>{attempt.recovery_reason}</p> : null}
        {attempt.stop_requested ? <p>{T("setup_stop_intent")}</p> : null}
        {attempt.state === "exited_incomplete" || attempt.state === "cancel_requested" ? <p>{T("setup_outcome_unknown")}</p> : null}
        {!ready && !live ? <p>{T("setup_retry_hint")}</p> : null}
        {live ? <div className="inline-actions">
          <Button variant="secondary" onClick={() => setObserving(observing === attempt.session_id ? "" : attempt.session_id)}>{T(observing === attempt.session_id ? "setup_hide_terminal" : "setup_observe")}</Button>
          <Button variant="secondary" disabled={busy || attempt.stop_requested} onClick={() => act(() => onCancel(attempt.session_id))}>{T("setup_cancel")}</Button>
        </div> : null}
      </section> : null}
      {live && observing === attempt.session_id ? <section className="log-panel log-panel--fill" aria-label={T("setup_observe")}>
        <XTerm sessionId={attempt.session_id} inputEnabled={false} client={client} />
      </section> : null}
      {err ? <p className="err" role="alert">{err}</p> : null}
      {!ready ? <form className="panel plan-body" onSubmit={(e) => {
        e.preventDefault();
        if (busy || !confirmed || !configValid || !notesValid || !accountReady || workspace.setup?.state === "invalid_workspace") return;
        void act(() => onStart(draft));
      }}>
        {field("stack", T("setup_stack"))}
        {field("methodology", T("setup_method"))}
        {field("test", T("setup_test"))}
        {field("build", T("setup_build"))}
        {field("install", T("setup_install"))}
        <Field label={T("setup_execution")}>
          <Select required value={draft.config.execution} onChange={(e) => {
            const value = e.target.value;
            if (value === "" || value === "sequential" || value === "parallel") edit({ ...draft, config: { ...draft.config, execution: value } });
          }}>
            <option value="">{T("setup_choose")}</option>
            <option value="sequential">{T("setup_sequential")}</option><option value="parallel">{T("setup_parallel")}</option>
          </Select>
        </Field>
        <Field label={T("setup_worktree")}>
          <Select required value={draft.config.worktree} onChange={(e) => {
            const value = e.target.value;
            if (value === "" || value === "off" || value === "medium+" || value === "always") edit({ ...draft, config: { ...draft.config, worktree: value } });
          }}>
            <option value="">{T("setup_choose")}</option>
            <option value="off">{T("setup_off")}</option><option value="medium+">{T("setup_medium")}</option><option value="always">{T("setup_always")}</option>
          </Select>
        </Field>
        {field("template", T("setup_template"))}
        <p className="muted">{T("setup_field_rules")}</p>
        <Field label={T("setup_notes")}>
          <textarea className="lyra-input" value={draft.brief} aria-invalid={!notesValid} onChange={(e) => edit({ ...draft, brief: e.target.value })} />
        </Field>
        <p className="muted">{T("setup_notes_rules")}</p>
        <Field label={T("setup_account")}>
          <Select required value={draft.account_id} onChange={(e) => edit({ ...draft, account_id: e.target.value })}>
            <option value="">{T("setup_choose")}</option>
            {draft.account_id && !selected ? <option value={draft.account_id}>{draft.account_id} · {T("setup_account_missing")}</option> : null}
            {accounts.map((a) => <option key={a.id} value={a.id}>{a.label} · {a.adapter} · {a.id} · {setupAccountReady(a) ? T("setup_account_ready") : a.readiness?.code || T("unknown")}</option>)}
          </Select>
        </Field>
        {selected ? <p>{selected.readiness?.code || T("unknown")} · {selected.readiness?.checked_at || "—"}</p> : null}
        {!accountReady ? <p>{T("setup_account_repair")}</p> : null}
        <label className="inject-toggle">
          <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} />{T("setup_confirm")}
        </label>
        <Button type="submit" disabled={busy || !confirmed || !configValid || !notesValid || !accountReady || workspace.setup?.state === "invalid_workspace"}>{T("start_setup")}</Button>
      </form> : null}
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
  onLogin,
}: {
  accounts: Account[];
  limits: Limit[];
  sessions: Session[];
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  onSaved: () => void;
  onLogin: (a: Account) => void;
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
                  {a.authed ? "● " : a.connected ? "◐ " : "○ "}
                  {a.label}
                </span>
                <div className="muted" style={{ fontSize: 11.5 }}>
                  {a.authed ? T("signed_in") : a.connected ? T("folder_only") : "—"}
                </div>
                {a.connected && !a.authed ? (
                  <button
                    type="button"
                    className="linkish"
                    onClick={(e) => {
                      e.stopPropagation();
                      onLogin(a);
                    }}
                  >
                    {T("sign_in")}
                  </button>
                ) : null}
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
              <span className="mono muted">
                {lim?.origin || lim?.source || "—"}
                {lim?.observed_at ? <div>{relTime(lim.observed_at)}</div> : null}
              </span>
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
