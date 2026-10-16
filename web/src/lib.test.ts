import { expect, test, vi } from "vitest";
import { api, journeyMutation, stopExecution, executionDelivery, ExecutionStopError, type ExecutionStopResult } from "./lib";

test("propagates the actual failed API response body", async () => {
  vi.stubGlobal("fetch", async () => new Response("workspace root does not exist", { status: 400 }));
  await expect(api("/v0/workspaces", { method: "POST", body: JSON.stringify({ name: "missing", root: "/missing" }) }))
    .rejects.toThrow("workspace root does not exist");
});

test("rejects unauthorized responses and clears the stale credential", async () => {
  localStorage.setItem("bt-token", "expired-synthetic-token");
  vi.stubGlobal("fetch", async () => new Response("unauthorized", { status: 401 }));
  await expect(api("/v0/accounts")).rejects.toThrow("invalid token. Run console --web to open with the token for this home");
  expect(localStorage.getItem("bt-token")).toBeNull();
});

// The companion redirect.test.ts exercises actual fetch redirects over HTTP.
test.each(["journey", "POST", "PUT"])("%s requires transport rejection of redirects", async method => {
  let request: Request | undefined;
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    request = new Request(`http://localhost${path}`, init);
    return Response.json({ effect: "recorded" });
  });
  const body = { mission_id: "m1", version: "exact-version" };
  if (method === "journey") await journeyMutation("/mutate", body);
  else await api("/mutate", { method, body: JSON.stringify(body), redirect: "follow" });
  expect(request?.redirect).toBe("error");
  expect(await request?.json()).toEqual(body);
});

const stopTarget = { mission_id: "m1", version: "a".repeat(64), binding: { workspace_id: "w1", workspace_root: "/work", canonical_root: "/work", plan_path: ".batuta/plans/p.md", plan_slug: "p", plan_digest: "digest" } };
const stopRequest = { target: stopTarget, approval_id: "a1", delivery: "console-7be5a6", run_id: "b".repeat(64) };

test("native Stop refuses invalid authority before dispatch", async () => {

  const fetch = vi.fn(async () => Response.json({ ...stopRequest, effect: "pending", finalized: false, durable: false }, { status: 202 }));
  vi.stubGlobal("fetch", fetch);
  await expect(stopExecution({ ...stopRequest, run_id: "invalid" })).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});

async function exactStop() {
  return { ...stopRequest, delivery: await executionDelivery(stopRequest.approval_id, stopTarget) };
}

function stopReply(request: typeof stopRequest, effect: ExecutionStopResult["effect"]): ExecutionStopResult {
  return { ...request, effect, finalized: effect === "completed" || effect === "uncertain", durable: effect === "completed",
    ...(effect === "completed" ? { state: { target: request.target, approval_id: request.approval_id, delivery: request.delivery, active: false, bound: false } } : {}),
    ...(["uncertain", "rejected"].includes(effect) ? { error: { code: "native_failure", message: "original cause" } } : {}),
    ...(effect === "uncertain" ? { causes: ["journal I/O"] } : {}),
  };
}

test.each([["pending", 202], ["completed", 200], ["uncertain", 503], ["rejected", 409]] as const)("native Stop preserves %s HTTP %s", async (effect, status) => {
  const request = await exactStop();
  const reply = stopReply(request, effect);
  localStorage.setItem("bt-token", "stop-token");
  const fetch = vi.fn(async (_path: string, init?: RequestInit) => {
    expect(init?.redirect).toBe("error");
    expect(init?.method).toBe("POST");
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer stop-token");
    expect(JSON.parse(String(init?.body))).toEqual(request);
    return Response.json(reply, { status });
  });
  vi.stubGlobal("fetch", fetch);
  if (reply.error) {
    await expect(stopExecution(request)).rejects.toMatchObject({ result: reply, status });
  } else await expect(stopExecution(request)).resolves.toEqual(reply);
  expect(fetch).toHaveBeenCalledTimes(1);
});

test.each(["malformed", "run", "mission", "status", "pending-final", "completed-active", "core", "lost", "abort"])("native Stop quarantines %s without replay", async kind => {
  const request = await exactStop();
  const controller = new AbortController();
  const reply = stopReply(request, kind.startsWith("completed") || kind === "core" ? "completed" : "pending");
  if (kind === "run") reply.run_id = "c".repeat(64);
  if (kind === "mission") reply.target = { ...request.target, mission_id: "foreign" };
  if (kind === "pending-final") reply.finalized = true;
  if (kind === "completed-active" && reply.state) reply.state.active = true;
  if (kind === "core") reply.core_state = "canceled";
  const fetch = vi.fn(async () => {
    if (kind === "lost") throw Error("lost reply");
    if (kind === "abort") controller.abort();
    return kind === "malformed" ? new Response("{") : Response.json(reply, { status: kind === "status" ? 200 : reply.effect === "completed" ? 200 : 202 });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(stopExecution(request, controller.signal)).rejects.toMatchObject({ result: { effect: "uncertain", run_id: request.run_id } });
  expect(fetch).toHaveBeenCalledTimes(1);
});

test("native Stop pre-canceled request dispatches nothing", async () => {
  const controller = new AbortController(); controller.abort();
  const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
  await expect(stopExecution(await exactStop(), controller.signal)).rejects.toBeInstanceOf(ExecutionStopError);
  expect(fetch).not.toHaveBeenCalled();
});

function completedOpened(request: typeof stopRequest): ExecutionStopResult {
  const gate = { required: true, cleared: false, delivery: request.delivery };
  return { ...stopReply(request, "completed"), core_state: "canceled", causes: ["context canceled"], state: {
    target: request.target, approval_id: request.approval_id, delivery: request.delivery, active: false, bound: true,
    opened_digest: "sha256:" + "d".repeat(64), gate,
    watch: { available: true, source: "current", scope: "mission", mission_id: "m1", workspace_id: "w1", delivery: request.delivery, header: { state: "canceled" }, progression: gate },
  } };
}

test.each(["valid", "unknown-field", "foreign-state", "wrong-core", "uncleared-done", "wrong-gate", "bad-opening", "durable-no-state", "falsy-run", "falsy-causes", "falsy-error", "falsy-state"])("native Stop final projection %s", async kind => {
  const request = await exactStop();
  const reply = completedOpened(request);
  if (kind === "falsy-run" && reply.state) Object.assign(reply.state, { run_id: 0 });
  if (kind === "falsy-causes") Object.assign(reply, { causes: false });
  if (kind === "falsy-error") Object.assign(reply, { error: false });
  if (kind === "falsy-state") Object.assign(reply, { effect: "pending", finalized: false, durable: false, state: false, core_state: undefined, causes: undefined });
  if (kind === "unknown-field") Object.assign(reply, { unexpected: true });
  if (kind === "foreign-state" && reply.state) reply.state.target = { ...request.target, mission_id: "other" };
  if (kind === "wrong-core") reply.core_state = "done";
  if (kind === "uncleared-done" && reply.state?.watch) { reply.core_state = "done"; reply.state.watch.header = { state: "done" }; }
  if (kind === "wrong-gate" && reply.state?.gate) reply.state.gate.delivery = "foreign";
  if (kind === "bad-opening" && reply.state) reply.state.opened_digest = "invalid";
  if (kind === "durable-no-state") delete reply.state;
  vi.stubGlobal("fetch", async () => Response.json(reply, { status: reply.effect === "pending" ? 202 : 200 }));
  if (kind === "valid") await expect(stopExecution(request)).resolves.toEqual(reply);
  else await expect(stopExecution(request)).rejects.toMatchObject({ result: { effect: "uncertain" } });
});
