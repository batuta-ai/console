import { useRef, useState } from "react";
import { api, type Mission, type Session } from "./lib";
import { T } from "./i18n";
import { Alert, Button } from "./ui";

export function MissionPlanning({ mission, sessions, client, onRefresh }: { mission: Mission; sessions: Session[]; client: string; onRefresh: () => void }) {
  const maestros = (mission.squad_snapshot?.members || []).filter(m => m.maestro);
  const maestro = maestros.length === 1 && maestros[0].id ? maestros[0] : undefined;
  const [busy, setBusy] = useState(false);
  const storageKey = `bt-planning:${mission.workspace_id}:${mission.id}`;
  const [blocked, setBlocked] = useState(() => sessionStorage.getItem(storageKey) === "inspect");
  const [error, setError] = useState("");
  const lock = useRef(false);
  const live = sessions.some(s => s.mission_id === mission.id && s.process === "running");

  async function plan() {
    if (!maestro || lock.current || blocked || live) return;
    lock.current = true;
    setBusy(true);
    setBlocked(true);
    setError("");
    try {
      sessionStorage.setItem(storageKey, "inspect");
      const started = await api<Session>("/v0/sessions", { method: "POST", body: JSON.stringify({ mission_id: mission.id, member_id: maestro.id, owner: "web", client, batuta: true }) });
      if (!started?.id || started.mission_id !== mission.id) throw new Error(T("journey_invalid_state"));
      onRefresh();
    } catch (e) {
      setError(`${T("journey_inspect_first")} ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }

  async function inspect() {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    try {
      const current = await api<Session[]>("/v0/sessions");
      const active = current.some(s => s.mission_id === mission.id && s.process === "running");
      sessionStorage.setItem(storageKey, active ? "inspect" : "ready");
      setBlocked(active);
      setError("");
      onRefresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }

  return <div>
    {maestro ? <p className="mono">{maestro.role} · {maestro.account_id} · {maestro.cli} · {maestro.model || T("inherit")} · {maestro.effort || T("inherit")}</p> : <Alert tone="warning">{T("journey_maestro_required")}</Alert>}
    {error ? <Alert tone="warning">{error}</Alert> : blocked && !live ? <Alert tone="warning">{T("journey_inspect_first")}</Alert> : null}
    {busy ? <p role="status">{T("journey_pending")}</p> : null}
    <div className="inline-actions">
      <Button disabled={!maestro || busy || blocked || live} onClick={plan}>{T("journey_plan_with", { role: maestro?.role || T("maestro") })}</Button>
      <Button variant="secondary" disabled={busy} onClick={inspect}>{T("journey_inspect_sessions")}</Button>
    </div>
  </div>;
}
