import { useEffect, useRef, useState } from "react";
import {
  api, executionLabel, journeyMutation, samePlan, sameQuestion, verifiedPlanText, verifyExecution, verifyReceipt,
  type AnswerResult, type ApprovalResult, type ExecutionResult, type ExecutionState, type Mission, type PlanApproval, type PlanDetail, type QuestionIdentity, type Watch,
} from "./lib";
import { T } from "./i18n";
import { Alert, Button, Field, Input, Stepper } from "./ui";

export function MissionJourney({ mission, refreshVersion, onWatch }: { mission: Mission; refreshVersion: number; onWatch: (watch: Watch | null) => void }) {
  const [path, setPath] = useState(mission.binding?.plan_path || "");
  const [plan, setPlan] = useState<PlanDetail | null>(null);
  const [text, setText] = useState("");
  const [receipt, setReceipt] = useState<PlanApproval | null>(null);
  const [execution, setExecution] = useState<ExecutionState | null>(null);
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [answer, setAnswer] = useState("");
  const [recordedQuestion, setRecordedQuestion] = useState<QuestionIdentity | null>(null);
  const current = useRef<{ mission: Mission; receipt: PlanApproval | null; execution: ExecutionState | null }>({ mission, receipt: null, execution: null });
  const lock = useRef(false);
  const generation = useRef(0);
  const watchCallback = useRef(onWatch);
  watchCallback.current = onWatch;
  const base = `/v0/missions/${encodeURIComponent(mission.id)}`;
  const workspaceQuery = `workspace_id=${encodeURIComponent(mission.workspace_id)}`;
  const storageKey = `bt-journey:${mission.workspace_id}:${mission.id}`;
  const snapshotKey = `${storageKey}:approved`;
  const executionPendingKey = `${storageKey}:execution-pending`;

  function retainSnapshot() {
    const state = current.current.execution;
    return !!(state?.active || state?.bound || state?.opened_digest || current.current.mission.binding?.delivery_id || sessionStorage.getItem(executionPendingKey));
  }

  function markBlocked(value: boolean) {
    // Persist uncertainty across navigation/reload; storage failure is visible and fail-closed.
    sessionStorage.setItem(storageKey, value ? "inspect" : "ready");
    setBlocked(value);
  }

  function showExecution(state: ExecutionState | null) {
    current.current.execution = state;
    setExecution(state);
    watchCallback.current(state?.watch || null);
  }

  async function readExecution(approved: PlanApproval, ticket: number) {
    const read = await api<PlanApproval>(`${base}/plan/approvals/${encodeURIComponent(approved.id)}?${workspaceQuery}`);
    await verifyReceipt(read, approved.identity, approved.content, current.current.mission);
    if (read.id !== approved.id) throw new Error(T("journey_invalid_plan"));
    const state = await api<ExecutionState>(`${base}/execution?${workspaceQuery}&approval_id=${encodeURIComponent(approved.id)}`);
    verifyExecution(state, read, current.current.execution);
    const binding = current.current.mission.binding;
    if (binding?.delivery_id && (state.delivery !== binding.delivery_id || state.opened_digest !== binding.opened_digest)) throw new Error(T("journey_invalid_state"));
    if (ticket === generation.current) showExecution(state);
  }

  async function readPlan(ticket: number, snapshot?: PlanDetail) {
    const detail = snapshot || await api<PlanDetail>(`${base}/plan?${workspaceQuery}`);
    if (!detail || (snapshot && !detail.approval)) throw new Error(T("journey_invalid_plan"));
    const original = await verifiedPlanText(detail.identity, detail.content, current.current.mission);
    if (typeof detail.title !== "string" || typeof detail.status !== "string" || !Array.isArray(detail.tasks) || detail.tasks.some(task => !task || typeof task.id !== "string" || typeof task.title !== "string" ||
      [task.scope, task.dependencies, task.accept].some(values => values !== null && (!Array.isArray(values) || values.some(value => typeof value !== "string"))))) throw new Error(T("journey_invalid_plan"));
    if (detail.approval) await verifyReceipt(detail.approval, detail.identity, detail.content, current.current.mission);
    if (ticket !== generation.current) return;
    if (current.current.receipt?.id !== detail.approval?.id || !detail.approval) showExecution(null);
    current.current.receipt = detail.approval || null;
    setPlan(detail);
    setText(original);
    setReceipt(detail.approval || null);
    setPath(detail.identity.binding.plan_path);
    if (detail.approval) {
      await readExecution(detail.approval, ticket);
      if (ticket === generation.current) sessionStorage.setItem(snapshotKey, JSON.stringify(detail));
    }
  }

  async function inspect(manual = false) {
    if (lock.current) return;
    const ticket = ++generation.current;
    if (mission.binding) current.current.mission = mission;
    lock.current = true;
    setBusy(true);
    setUnavailable(true);
    watchCallback.current(null);
    try {
      if (manual) {
        const missions = await api<Mission[]>("/v0/missions");
        const exact = missions.find(m => m.id === mission.id && m.workspace_id === mission.workspace_id);
        if (!exact) throw new Error(T("journey_invalid_state"));
        current.current.mission = exact;
      }
      const saved = sessionStorage.getItem(snapshotKey);
      if (current.current.receipt) await readExecution(current.current.receipt, ticket);
      else if (saved) await readPlan(ticket, JSON.parse(saved));
      if (ticket !== generation.current) return;
      if (!retainSnapshot() && current.current.mission.binding) {
        // An unopened historical receipt does not establish current source eligibility.
        // Once its exact state is inspected, rebinding remains possible if the source fails to load.
        if (manual) markBlocked(false);
        await readPlan(ticket);
      } else if (!current.current.receipt && current.current.mission.binding) await readPlan(ticket);
      if (ticket !== generation.current) return;
      setUnavailable(false);
      if (manual) {
        markBlocked(false);
        setError("");
      } else setBlocked(sessionStorage.getItem(storageKey) === "inspect");
    } catch (e) {
      if (ticket === generation.current) {
        setUnavailable(true);
        setError(e instanceof Error ? e.message : String(e));
        watchCallback.current(null);
      }
    } finally {
      if (ticket === generation.current) {
        lock.current = false;
        setBusy(false);
      }
    }
  }

  const inspectRef = useRef(inspect);
  inspectRef.current = inspect;
  useEffect(() => {
    void inspectRef.current();
  }, [refreshVersion]);
  useEffect(() => () => { ++generation.current; lock.current = false; }, []);

  async function mutate(action: (ticket: number) => Promise<void>) {
    if (lock.current || blocked) return;
    const ticket = ++generation.current;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      markBlocked(true);
      await action(ticket);
      if (ticket !== generation.current) return;
      markBlocked(false);
      setUnavailable(false);
    } catch (e) {
      if (ticket === generation.current) {
        setBlocked(true);
        setError(`${T("journey_inspect_first")} ${e instanceof Error ? e.message : String(e)}`);
      }
    } finally {
      if (ticket === generation.current) {
        lock.current = false;
        setBusy(false);
      }
    }
  }

  async function bind(ticket: number) {
    const bound = await api<Mission>(`${base}/binding`, { method: "PUT", body: JSON.stringify({ workspace_id: mission.workspace_id, plan_path: path }) });
    if (bound?.id !== mission.id || bound.workspace_id !== mission.workspace_id || !bound.binding || bound.binding.workspace_id !== mission.workspace_id || bound.binding.plan_path !== path) throw new Error(T("journey_invalid_plan"));
    if (ticket !== generation.current) return;
    current.current.mission = bound;
    current.current.receipt = null;
    sessionStorage.removeItem(snapshotKey);
    setPlan(null);
    setReceipt(null);
    showExecution(null);
    await readPlan(ticket);
  }

  async function approve(ticket: number) {
    if (!plan || receipt) return;
    const result = await journeyMutation<ApprovalResult>(`${base}/plan/approvals`, plan.identity);
    if (!result || !samePlan(result.target, plan.identity) || result.effect !== "recorded" || !result.receipt) throw new Error(result?.error?.message || T("journey_invalid_plan"));
    await verifyReceipt(result.receipt, plan.identity, plan.content, current.current.mission);
    if (ticket !== generation.current) return;
    current.current.receipt = result.receipt;
    setReceipt(result.receipt);
    sessionStorage.setItem(`${snapshotKey}:${result.receipt.id}`, JSON.stringify({ ...plan, approval: result.receipt }));
    sessionStorage.setItem(snapshotKey, JSON.stringify({ ...plan, approval: result.receipt }));
    await readExecution(result.receipt, ticket);
  }

  async function execute(ticket: number, resume: boolean) {
    if (!receipt || !execution) return;
    const body = { target: receipt.identity, approval_id: receipt.id };
    if (!resume) sessionStorage.setItem(executionPendingKey, receipt.id);
    const result = await journeyMutation<ExecutionResult>(`${base}/execution${resume ? "/resume" : ""}`, resume ? { ...body, delivery: execution.delivery, opened_digest: execution.opened_digest } : body);
    if (!resume && result && samePlan(result.target, receipt.identity) && result.approval_id === receipt.id && result.effect === "rejected") sessionStorage.removeItem(executionPendingKey);
    if (!result || !samePlan(result.target, receipt.identity) || result.approval_id !== receipt.id || result.effect !== "started" || result.delivery !== execution.delivery ||
      !result.opened_digest || (resume && result.opened_digest !== execution.opened_digest)) throw new Error(result?.error?.message || T("journey_invalid_state"));
    await readExecution(receipt, ticket);
  }

  async function sendAnswer(ticket: number) {
    const question = execution?.watch?.pending_question;
    if (!question || !receipt || (recordedQuestion && sameQuestion(recordedQuestion, question.target))) return;
    const result = await journeyMutation<AnswerResult>("/v0/watch/answer", { ...question.target, text: answer });
    if (!result || !sameQuestion(result.target, question.target) || result.delivery !== execution?.delivery || result.effect !== "recorded") throw new Error(result?.error?.message || T("journey_invalid_state"));
    if (ticket !== generation.current) return;
    setRecordedQuestion(question.target);
    setNotice(T("journey_answer_recorded"));
    setAnswer("");
    await readExecution(receipt, ticket);
  }

  const disabled = busy || blocked || unavailable;
  const label = executionLabel(execution, !!receipt, unavailable || blocked);
  const question = !unavailable ? execution?.watch?.pending_question : undefined;
  const canStart = !!receipt && !!execution && !execution.active && !execution.bound && !execution.opened_digest;
  const canResume = !!receipt && !!execution?.opened_digest && !execution.active && !question && execution.watch?.header?.state !== "done";
  const bindingLocked = busy || blocked || retainSnapshot() || (!!receipt && !execution);

  return <div>
    <Stepper steps={[T("plan_understand"), T("plan_decompose"), T("plan_write"), T("plan_approve"), T("plan_execute")]} current={execution?.opened_digest ? 5 : receipt ? 4 : plan ? 3 : 1} />
    <p aria-label={T("journey_status")}>{label}</p>
    {busy ? <p role="status">{T("journey_pending")}</p> : null}
    {error ? <Alert tone="warning">{error}</Alert> : blocked ? <Alert tone="warning">{T("journey_inspect_first")}</Alert> : null}
    {notice ? <Alert tone="success">{notice}</Alert> : null}
    <Field label={T("journey_plan_path")}><Input value={path} disabled={bindingLocked} onChange={e => setPath(e.target.value)} /></Field>
    <div className="inline-actions">
      <Button disabled={bindingLocked || !path.trim()} onClick={() => mutate(bind)}>{T("journey_bind")}</Button>
      <Button variant="secondary" disabled={busy} onClick={() => inspect(true)}>{T("journey_inspect")}</Button>
    </div>
    {plan ? <>
      <h3>{plan.title}</h3>
      <p className="mono">{plan.identity.version}</p>
      <pre aria-label={T("journey_original")} style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{text}</pre>
      <a href={`data:application/octet-stream;base64,${plan.content}`} download={plan.identity.binding.plan_path.split("/").pop()}>{T("journey_download")}</a>
      {plan.tasks.map(task => <div className="panel" key={task.id}>
        <h4>{task.id} · {task.title}</h4>
        <dl>
          <dt>{T("journey_scope")}</dt><dd style={{ whiteSpace: "pre-wrap" }}>{(task.scope || []).join("\n")}</dd>
          <dt>{T("journey_dependencies")}</dt><dd>{(task.dependencies || []).join(", ") || "—"}</dd>
          <dt>{T("journey_acceptance")}</dt><dd style={{ whiteSpace: "pre-wrap" }}>{(task.accept || []).join("\n")}</dd>
        </dl>
      </div>)}
      {receipt ? <p>{T("journey_receipt")} <span className="mono">{receipt.id}</span></p> : null}
      <div className="inline-actions">
        <Button disabled={disabled || !!receipt || !!plan.identity.binding.delivery_id || !["proposed", "approved"].includes(plan.status)} onClick={() => mutate(approve)}>{T("journey_approve")}</Button>
        <Button disabled={disabled || !canStart} onClick={() => mutate(ticket => execute(ticket, false))}>{T("journey_start")}</Button>
        <Button disabled={disabled || !canResume} onClick={() => mutate(ticket => execute(ticket, true))}>{T("journey_resume")}</Button>
      </div>
    </> : null}
    {execution ? <p className="mono">{execution.delivery} · {execution.opened_digest || "—"}</p> : null}
    {execution?.gate?.reason ? <p>{execution.gate.reason}</p> : null}
    {question ? <div>
      <p>{question.prompt}</p>
      <p className="mono">{question.target.task} · {question.target.execution} · {question.target.question_id} · {question.target.opened_digest}</p>
      <Field label={T("journey_answer")}><Input value={answer} onChange={e => setAnswer(e.target.value)} /></Field>
      <p>{T("answer_no_paid")}</p>
      <Button disabled={disabled || !answer.trim() || (!!recordedQuestion && sameQuestion(recordedQuestion, question.target))} onClick={() => mutate(sendAnswer)}>{T("send_answer")}</Button>
    </div> : null}
  </div>;
}
