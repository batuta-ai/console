import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  api, executionLabel, journeyMutation, stopExecution, ExecutionStopError, validRunID, executionDelivery, samePlan, sameQuestion, verifiedPlanText, verifyExecution, verifyReceipt,
  type ExecutionStopRequest, type ExecutionStopResult, type AnswerResult, type ApprovalResult, type ExecutionResult, type ExecutionState, type Mission, type PlanApproval, type PlanDetail, type QuestionIdentity, type Watch,
} from "./lib";
import { bounded, READ_INTERVAL } from "./sync";
import { T } from "./i18n";
import { Alert, Button, Field, Input, Stepper } from "./ui";

type JourneyProps = { mission: Mission; refreshVersion: number; onWatch: (watch: Watch | null, state?: ExecutionState | null) => void; children?: (journey: ReactNode) => ReactNode };

export function MissionJourney(props: Omit<JourneyProps, "mission"> & { mission: Mission | null }) {
  if (!props.mission) return props.children?.(null);
  return <Journey key={`${props.mission.workspace_id}:${props.mission.id}`} {...props} mission={props.mission} />;
}

function Journey({ mission, refreshVersion, onWatch, children }: JourneyProps) {
  const [path, setPath] = useState(mission.binding?.plan_path || "");
  const [plan, setPlan] = useState<PlanDetail | null>(null);
  const [text, setText] = useState("");
  const [receipt, setReceipt] = useState<PlanApproval | null>(null);
  const [execution, setExecution] = useState<ExecutionState | null>(null);
  const [stopCandidate, setStopCandidate] = useState<ExecutionStopRequest | null>(null);
  const [selectedStop, setSelectedStop] = useState<ExecutionStopRequest | null>(null);
  const [stopResult, setStopResult] = useState<ExecutionStopResult | null>(null);
  const [stopError, setStopError] = useState("");
  const [stopNotSent, setStopNotSent] = useState(false);
  const restoredStop = useRef(false);
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [answerDraft, setAnswerDraft] = useState<{ target: QuestionIdentity; text: string } | null>(null);
  const [recordedQuestion, setRecordedQuestion] = useState<QuestionIdentity | null>(null);
  const current = useRef<{ mission: Mission; receipt: PlanApproval | null; execution: ExecutionState | null }>({ mission, receipt: null, execution: null });
  const lock = useRef(false);
  const generation = useRef(0);
  const operation = useRef<AbortController | null>(null);
  const watchCallback = useRef(onWatch);
  watchCallback.current = onWatch;
  const base = `/v0/missions/${encodeURIComponent(mission.id)}`;
  const workspaceQuery = `workspace_id=${encodeURIComponent(mission.workspace_id)}`;
  const storageKey = `bt-journey:${mission.workspace_id}:${mission.id}`;
  const snapshotKey = `${storageKey}:approved`;
  const stopKey = `${storageKey}:native-stop`;
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

  function showExecution(state: ExecutionState | null, verifiedStop = false) {
    const target = state?.watch?.pending_question?.target;
    setAnswerDraft(draft => draft && target && sameQuestion(draft.target, target) ? draft : null);
    setStopCandidate(verifiedStop && state?.active && validRunID(state.run_id) ? structuredClone({ target: state.target, approval_id: state.approval_id, delivery: state.delivery, run_id: state.run_id }) : null);
    current.current.execution = state;
    setExecution(state);
    watchCallback.current(state?.watch || null, state);
  }

  async function readExecution(approved: PlanApproval, ticket: number, signal: AbortSignal) {
    const read = await api<PlanApproval>(`${base}/plan/approvals/${encodeURIComponent(approved.id)}?${workspaceQuery}`, { signal });
    signal.throwIfAborted();
    await verifyReceipt(read, approved.identity, approved.content, current.current.mission);
    signal.throwIfAborted();
    if (ticket !== generation.current || signal.aborted) return;
    if (read.id !== approved.id) throw new Error(T("journey_invalid_plan"));
    if (!restoredStop.current) {
      const saved = sessionStorage.getItem(stopKey);
      if (saved) {
        const record: { request: ExecutionStopRequest; result: ExecutionStopResult | null } = JSON.parse(saved);
        if (record.request?.approval_id && record.request.approval_id !== approved.id &&
          record.request.target?.mission_id === mission.id && record.request.target.binding?.workspace_id === mission.workspace_id &&
          validRunID(record.request.run_id) && record.request.delivery === await executionDelivery(record.request.approval_id, record.request.target)) {
          signal.throwIfAborted();
          if (ticket !== generation.current) return;
          // Preserve the old receipt's history without assigning it authority over this receipt.
          sessionStorage.setItem(`${stopKey}:${record.request.approval_id}`, saved);
          sessionStorage.removeItem(stopKey);
        } else {
          if (!record.request || !samePlan(record.request.target, approved.identity) || record.request.approval_id !== approved.id || !validRunID(record.request.run_id) || record.request.delivery !== await executionDelivery(approved.id, approved.identity)) throw new Error(T("journey_invalid_state"));
          signal.throwIfAborted();
          if (ticket !== generation.current) return;
          setSelectedStop(record.request);
          setStopResult(record.result);
        }
      }
      restoredStop.current = true;
    }
    const state = await api<ExecutionState>(`${base}/execution?${workspaceQuery}&approval_id=${encodeURIComponent(approved.id)}`, { signal });
    signal.throwIfAborted();
    verifyExecution(state, read, current.current.execution);
    const binding = current.current.mission.binding;
    if (binding?.delivery_id && (state.delivery !== binding.delivery_id || state.opened_digest !== binding.opened_digest)) throw new Error(T("journey_invalid_state"));
    const verifiedStop = state.active && state.delivery === await executionDelivery(read.id, read.identity);
    signal.throwIfAborted();
    if (ticket === generation.current) showExecution(state, verifiedStop);
  }

  async function readPlan(ticket: number, signal: AbortSignal, snapshot?: PlanDetail) {
    const detail = snapshot || await api<PlanDetail>(`${base}/plan?${workspaceQuery}`, { signal });
    signal.throwIfAborted();
    if (!detail || (snapshot && !detail.approval)) throw new Error(T("journey_invalid_plan"));
    const original = await verifiedPlanText(detail.identity, detail.content, current.current.mission);
    signal.throwIfAborted();
    if (typeof detail.title !== "string" || typeof detail.status !== "string" || !Array.isArray(detail.tasks) || detail.tasks.some(task => !task || typeof task.id !== "string" || typeof task.title !== "string" ||
      [task.scope, task.dependencies, task.accept].some(values => values !== null && (!Array.isArray(values) || values.some(value => typeof value !== "string"))))) throw new Error(T("journey_invalid_plan"));
    if (detail.approval) await verifyReceipt(detail.approval, detail.identity, detail.content, current.current.mission);
    if (ticket !== generation.current || signal.aborted) return;
    if (current.current.receipt?.id !== detail.approval?.id || !detail.approval) showExecution(null);
    current.current.receipt = detail.approval || null;
    setPlan(detail);
    setText(original);
    setReceipt(detail.approval || null);
    setPath(detail.identity.binding.plan_path);
    if (detail.approval) {
      await readExecution(detail.approval, ticket, signal);
      if (ticket === generation.current) sessionStorage.setItem(snapshotKey, JSON.stringify(detail));
    }
  }

  async function inspect(manual = false) {
    if (lock.current) return;
    const ticket = ++generation.current;
    if (mission.binding) current.current.mission = mission;
    lock.current = true;
    setBusy(true);
    const controller = new AbortController();
    operation.current = controller;
    try {
      await bounded(async signal => {
        const missions = await api<Mission[]>("/v0/missions", { signal });
        signal.throwIfAborted();
        const exact = missions.find(m => m.id === mission.id && m.workspace_id === mission.workspace_id);
        if (!exact) throw new Error(T("journey_invalid_state"));
        current.current.mission = exact;
        const saved = sessionStorage.getItem(snapshotKey);
        if (current.current.receipt) await readExecution(current.current.receipt, ticket, signal);
        else if (saved) await readPlan(ticket, signal, JSON.parse(saved));
        if (ticket !== generation.current || signal.aborted) return;
        if (!retainSnapshot() && current.current.mission.binding) {
          // An unopened historical receipt does not establish current source eligibility.
          // Once its exact state is inspected, rebinding remains possible if the source fails to load.
          if (manual) markBlocked(false);
          await readPlan(ticket, signal);
        } else if (!current.current.receipt && current.current.mission.binding) await readPlan(ticket, signal);
        if (ticket !== generation.current || signal.aborted) return;
        setUnavailable(false);
        setError("");
        if (manual) markBlocked(false);
        else setBlocked(sessionStorage.getItem(storageKey) === "inspect");
      }, controller.signal);
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
  useEffect(() => {
    const timer = window.setInterval(() => { void inspectRef.current(); }, READ_INTERVAL);
    return () => {
      ++generation.current;
      operation.current?.abort();
      lock.current = false;
      window.clearInterval(timer);
      watchCallback.current(null);
    };
  }, []);

  async function mutate(action: (ticket: number, signal: AbortSignal) => Promise<void>) {
    if (lock.current || blocked) return;
    const ticket = ++generation.current;
    lock.current = true;
    setBusy(true);
    setError("");
    const controller = new AbortController();
    operation.current = controller;
    try {
      markBlocked(true);
      await bounded(signal => action(ticket, signal), controller.signal);
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

  async function bind(ticket: number, signal: AbortSignal) {
    const bound = await api<Mission>(`${base}/binding`, { signal, method: "PUT", body: JSON.stringify({ workspace_id: mission.workspace_id, plan_path: path }) });
    if (bound?.id !== mission.id || bound.workspace_id !== mission.workspace_id || !bound.binding || bound.binding.workspace_id !== mission.workspace_id || bound.binding.plan_path !== path) throw new Error(T("journey_invalid_plan"));
    if (ticket !== generation.current || signal.aborted) return;
    current.current.mission = bound;
    current.current.receipt = null;
    sessionStorage.removeItem(snapshotKey);
    setPlan(null);
    setReceipt(null);
    showExecution(null);
    await readPlan(ticket, signal);
  }

  async function approve(ticket: number, signal: AbortSignal) {
    if (!plan || receipt) return;
    const result = await journeyMutation<ApprovalResult>(`${base}/plan/approvals`, plan.identity, signal);
    if (!result || !samePlan(result.target, plan.identity) || result.effect !== "recorded" || !result.receipt) throw new Error(result?.error?.message || T("journey_invalid_plan"));
    if (ticket !== generation.current || signal.aborted) return;
    await verifyReceipt(result.receipt, plan.identity, plan.content, current.current.mission);
    if (ticket !== generation.current || signal.aborted) return;
    current.current.receipt = result.receipt;
    setReceipt(result.receipt);
    sessionStorage.setItem(`${snapshotKey}:${result.receipt.id}`, JSON.stringify({ ...plan, approval: result.receipt }));
    sessionStorage.setItem(snapshotKey, JSON.stringify({ ...plan, approval: result.receipt }));
    await readExecution(result.receipt, ticket, signal);
  }

  async function execute(ticket: number, resume: boolean, signal: AbortSignal) {
    if (!receipt || !execution) return;
    const body = { target: receipt.identity, approval_id: receipt.id };
    if (!resume) sessionStorage.setItem(executionPendingKey, receipt.id);
    const result = await journeyMutation<ExecutionResult>(`${base}/execution${resume ? "/resume" : ""}`, resume ? { ...body, delivery: execution.delivery, opened_digest: execution.opened_digest } : body, signal);
    if (ticket !== generation.current || signal.aborted) return;
    if (!resume && result && samePlan(result.target, receipt.identity) && result.approval_id === receipt.id && result.effect === "rejected") sessionStorage.removeItem(executionPendingKey);
    if (!result || !samePlan(result.target, receipt.identity) || result.approval_id !== receipt.id || result.effect !== "started" || result.delivery !== execution.delivery ||
      !validRunID(result.run_id) || !result.opened_digest || (resume && result.opened_digest !== execution.opened_digest)) throw new Error(result?.error?.message || T("journey_invalid_state"));
    const verifiedStop = result.delivery === await executionDelivery(receipt.id, receipt.identity);
    signal.throwIfAborted();
    if (ticket !== generation.current) return;
    if (verifiedStop) setStopCandidate(structuredClone({ ...body, delivery: result.delivery, run_id: result.run_id }));
    await readExecution(receipt, ticket, signal);
  }

  function selectStop() {
    if (lock.current || !stopCandidate) return;
    const exact = structuredClone(stopCandidate);
    try {
      sessionStorage.setItem(stopKey, JSON.stringify({ request: exact, result: null }));
      setSelectedStop(exact);
      setStopResult(null);
      setStopError("");
      setStopNotSent(false);
    } catch (e) {
      setBlocked(true);
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function submitStop() {
    if (lock.current || !selectedStop || (stopResult && stopResult.effect !== "pending")) return;
    const exact = structuredClone(selectedStop);
    const ticket = ++generation.current;
    const controller = new AbortController();
    operation.current = controller;
    lock.current = true;
    setBusy(true);
    let dispatched = false;
    let previouslyBlocked = blocked;
    const uncertain: ExecutionStopResult = { ...exact, effect: "uncertain", finalized: false, durable: false };
    try {
      previouslyBlocked = blocked || sessionStorage.getItem(storageKey) === "inspect";
      markBlocked(true);
      sessionStorage.setItem(stopKey, JSON.stringify({ request: exact, result: uncertain }));
      if (!stopResult) setStopResult(uncertain);
      setStopError("");
      setStopNotSent(false);
      dispatched = true;
      const result = await bounded(signal => stopExecution(exact, signal), controller.signal);
      if (ticket !== generation.current) return;
      sessionStorage.setItem(stopKey, JSON.stringify({ request: exact, result }));
      setStopResult(result);
      // A historical finalization must not project over a replacement admission.
      if (result.state && current.current.execution?.run_id === exact.run_id) showExecution(result.state);
      if (result.effect === "completed") markBlocked(previouslyBlocked);
    } catch (e) {
      if (ticket !== generation.current) return;
      if (!dispatched) {
        setBlocked(true);
        setStopNotSent(true);
        setStopError(e instanceof Error ? e.message : String(e));
        return;
      }
      const result = e instanceof ExecutionStopError ? e.result : uncertain;
      setBlocked(true);
      setStopResult(result);
      setStopError(e instanceof ExecutionStopError && e.result.error ? "" : e instanceof Error ? e.message : String(e));
      try {
        sessionStorage.setItem(stopKey, JSON.stringify({ request: exact, result }));
      } catch (storageError) {
        setStopError(message => `${message} ${storageError instanceof Error ? storageError.message : String(storageError)}`.trim());
      }
    } finally {
      if (ticket === generation.current) { lock.current = false; setBusy(false); }
    }
  }

  async function sendAnswer(ticket: number, signal: AbortSignal) {
    const question = execution?.watch?.pending_question;
    if (!question || !receipt || !answerDraft || !sameQuestion(answerDraft.target, question.target) || !answerDraft.text.trim() || (recordedQuestion && sameQuestion(recordedQuestion, question.target))) return;
    const result = await journeyMutation<AnswerResult>("/v0/watch/answer", { ...answerDraft.target, text: answerDraft.text }, signal);
    if (!result || !sameQuestion(result.target, question.target) || result.delivery !== execution?.delivery || result.effect !== "recorded") throw new Error(result?.error?.message || T("journey_invalid_state"));
    if (ticket !== generation.current || signal.aborted) return;
    setRecordedQuestion(question.target);
    setNotice(T("journey_answer_recorded"));
    setAnswerDraft(null);
    await readExecution(receipt, ticket, signal);
  }

  const disabled = busy || blocked || unavailable;
  const label = executionLabel(execution, !!receipt, unavailable || blocked);
  const question = !unavailable ? execution?.watch?.pending_question : undefined;
  const answer = question && answerDraft && sameQuestion(answerDraft.target, question.target) ? answerDraft.text : "";
  const canStart = !!receipt && !!execution && !execution.active && !execution.bound && !execution.opened_digest;
  const canResume = !!receipt && !!execution?.opened_digest && !execution.active && !question && execution.watch?.header?.state !== "done";
  const bindingLocked = busy || blocked || retainSnapshot() || (!!receipt && !execution);

  const content = <div>
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
        <Button disabled={disabled || !canStart} onClick={() => mutate((ticket, signal) => execute(ticket, false, signal))}>{T("journey_start")}</Button>
        <Button disabled={disabled || !canResume} onClick={() => mutate((ticket, signal) => execute(ticket, true, signal))}>{T("journey_resume")}</Button>
      </div>
    </> : null}
    {execution ? <p className="mono">{execution.delivery} · {execution.opened_digest || "—"}</p> : null}
    {stopCandidate && (!selectedStop || selectedStop.run_id !== stopCandidate.run_id) ? <Button variant="secondary" disabled={busy} onClick={selectStop}>{T("journey_stop")}</Button> : null}
    {selectedStop ? <div>
      <p className="mono">{selectedStop.delivery} · {selectedStop.run_id}</p>
      {stopNotSent ? <><p role="status">{T("journey_stop_not_sent")}</p>{stopError ? <Alert tone="warning">{stopError}</Alert> : null}</> : null}
      {!stopResult ? <><p>{T("journey_stop_confirm")}</p><Button disabled={busy} onClick={submitStop}>{T("journey_stop_confirm_action")}</Button></> : <>
        <p role="status">{T(stopResult.effect === "pending" ? "journey_stop_pending" : stopResult.effect === "completed" ? "journey_stop_completed" : stopResult.effect === "rejected" ? "journey_stop_rejected" : "journey_stop_uncertain")}</p>
        {stopResult.core_state ? <p>{T("journey_stop_core")} {stopResult.core_state}</p> : null}
        {stopResult.causes?.map((cause, index) => <p key={index}>{cause}</p>)}
        {stopResult.error ? <Alert tone="warning">{stopResult.error.code}: {stopResult.error.message}</Alert> : null}
        {stopError ? <Alert tone="warning">{stopError}</Alert> : null}
        {stopResult.effect === "pending" ? <Button variant="secondary" disabled={busy} onClick={submitStop}>{T("journey_stop_observe")}</Button> : null}
      </>}
    </div> : null}
    {execution?.gate?.reason ? <p>{execution.gate.reason}</p> : null}
    {question ? <div>
      <p>{question.prompt}</p>
      <p className="mono">{question.target.task} · {question.target.execution} · {question.target.question_id} · {question.target.opened_digest}</p>
      <Field label={T("journey_answer")}><Input value={answer} onChange={e => setAnswerDraft({ target: question.target, text: e.target.value })} /></Field>
      <p>{T("answer_no_paid")}</p>
      <Button disabled={disabled || !answer.trim() || (!!recordedQuestion && sameQuestion(recordedQuestion, question.target))} onClick={() => mutate(sendAnswer)}>{T("send_answer")}</Button>
    </div> : null}
  </div>;
  return children ? children(content) : content;
}
