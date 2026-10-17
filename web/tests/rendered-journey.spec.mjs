import assert from "node:assert/strict";
import { createNativeStopFixture } from "./native-stop-host-fixture.mjs";
import { spawn, spawnSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, realpath, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test as base, expect, chromium } from "@playwright/test";

const test = base.extend({
  journey: [async ({ viewport }, use) => {
    const fixture = await createJourney(viewport);
    try { await use(fixture); }
    finally { await fixture.close(); }
  }, { timeout: 180000 }],
});

test("two rendered immutable mission snapshots survive an owned daemon restart", async ({ journey: fixture }) => {
  await fixture.start();
  const { workspace, first, second } = await fixture.seed();
  assert.notEqual(first.id, second.id);
  assert.equal(first.title, "First rendered mission");
  assert.equal(second.title, "Second rendered mission");
  assert.equal(first.brief, "First immutable rendered objective");
  assert.equal(second.brief, "Second immutable rendered objective");
  assert.equal(first.squad_snapshot.version, 1);
  assert.equal(second.squad_snapshot.version, 2);
  const before = await fixture.catalog();
  for (const phase of ["before", "after"]) {
    if (phase === "after") {
      await fixture.stop();
      await fixture.start();
      assert.deepEqual(await fixture.catalog(), before);
    }
    const context = await fixture.openContext();
    let selectionFailure;
    try {
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      page.setDefaultNavigationTimeout(10000);
      await page.goto(fixture.origin, { waitUntil: "domcontentloaded" });
      await page.getByRole("main").getByRole("button", { name: new RegExp(workspace.name) })
        .filter({ has: page.getByText(workspace.root, { exact: true }) }).click();
      for (const mission of [second, first]) {
        await page.getByRole("button", { name: "Missions", exact: true }).click();
        const row = page.getByRole("button").filter({ has: page.getByText(mission.id, { exact: true }) });
        await expect(row.getByText(mission.title, { exact: true })).toBeVisible();
        await expect(row.getByText(mission.brief, { exact: true })).toBeVisible();
        await expect(row.getByText(mission.squad_snapshot.name, { exact: true })).toBeVisible();
        await row.click();
        await expect(page.getByRole("heading", { name: mission.title, exact: true })).toBeVisible();
        await expect(page.getByText(`${mission.id} · 0/2 journal tasks · no live session`, { exact: true })).toBeVisible();
        await expect(page.getByText(mission.brief, { exact: true })).toBeVisible();
        for (const member of mission.squad_snapshot.members) {
          await page.getByRole("button").filter({ has: page.getByText(member.role, { exact: true }) }).click();
          await expect(page.getByText(`${member.cli} · ${member.model}`, { exact: true })).toBeVisible();
        }
        console.log(JSON.stringify({ phase, mission: mission.id, title: mission.title, brief: mission.brief, squadVersion: mission.squad_snapshot.version }));
      }
    } catch (error) {
      selectionFailure = error;
    } finally {
      try { await bounded(context.close(), 10000, "context close"); }
      catch (error) {
        selectionFailure = selectionFailure ? new AggregateError([selectionFailure, error], "Selection and context cleanup failed") : error;
      }
    }
    if (selectionFailure) throw selectionFailure;
  }
});

async function bounded(promise, milliseconds, label) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`${label} exceeded ${milliseconds}ms`)), milliseconds);
    })]);
  } finally { clearTimeout(timer); }
}

async function createJourney(viewport) {
  const repository = fileURLToPath(new URL("../../", import.meta.url));
  const temporaryRoot = await realpath(os.tmpdir());
  // Reject a custom TMPDIR inside any checkout, including a Git worktree.
  for (let dir = temporaryRoot; ; dir = path.dirname(dir)) {
    try {
      await realpath(path.join(dir, ".git"));
      throw new Error("Temporary root must be outside Git");
    } catch (error) { if (error.code !== "ENOENT") throw error; }
    if (dir === path.dirname(dir)) break;
  }
  const temporary = await mkdtemp(path.join(temporaryRoot, "console-rendered-"));
  const home = path.join(temporary, "runtime");
  const userHome = path.join(temporary, "user");
  const workspaceRoot = path.join(temporary, "workspace");
  const binary = path.join(temporary, "consoled");
  const environment = {
    HOME: userHome, PATH: "", TMPDIR: temporary, TMP: temporary, TEMP: temporary,
    BATUTA_LANG: "en", LANG: "en_US.UTF-8",
    XDG_CONFIG_HOME: path.join(userHome, ".config"),
    XDG_DATA_HOME: path.join(userHome, ".local", "share"),
    XDG_STATE_HOME: path.join(userHome, ".local", "state"),
    XDG_CACHE_HOME: path.join(userHome, ".cache"),
  };
  let owned;
  let browser;
  let token;
  let origin;
  let seeded;
  let cleaning;
  let interrupted;
  let buildCompletionUnverified = false;

  const stop = async () => {
    if (!owned || owned.joined) return;
    const current = owned;
    const premature = current.result !== undefined;
    let forced = false;
    if (!premature) current.child.kill("SIGTERM");
    let result;
    try { result = await bounded(current.closed, 7000, "daemon graceful drain"); }
    catch (error) {
      forced = true;
      current.child.kill("SIGKILL");
      try { result = await bounded(current.closed, 3000, "daemon forced drain"); }
      catch (drainError) { throw new AggregateError([error, drainError], "Owned daemon could not be joined; runtime retained"); }
    }
    current.joined = true;
    assert(!premature && !forced && !current.error && result.code === 0 && result.signal === null,
      `daemon stop failed: premature=${premature} forced=${forced} exit=${result.code} signal=${result.signal} error=${current.error?.message || "none"}`);
    console.log("Owned daemon closed and joined with exit 0");
  };

  const close = () => cleaning ??= (async () => {
    const errors = [];
    if (buildCompletionUnverified) {
      errors.push(new Error("Go build completion unverified; compiler children may remain; owned runtime retained"));
    }
    if (browser) {
      try { await bounded(browser.close(), 10000, "browser close"); }
      catch (error) { errors.push(error); }
    }
    try { await stop(); } catch (error) { errors.push(error); }
    if ((!owned || owned.joined) && errors.length === 0) {
      try { await rm(temporary, { recursive: true, force: true }); }
      catch (error) { errors.push(error); }
    }
    process.off("SIGINT", onSignal);
    process.off("SIGTERM", onSignal);
    token = undefined;
    if (interrupted) errors.push(new Error(`Journey interrupted by ${interrupted}`));
    if (errors.length) throw new AggregateError(errors, "Journey cleanup failed; owned runtime may be retained");
  })();
  const onSignal = signal => {
    interrupted = signal;
    // The fixture teardown reports this failure; never force-exit before joining.
    void close().catch(error => { interrupted = error.message; });
  };
  process.on("SIGINT", onSignal);
  process.on("SIGTERM", onSignal);

  const request = async (endpoint, method = "GET", body) => {
    assert(owned && !owned.result && !interrupted, "Owned daemon must be live");
    const response = await fetch(origin + endpoint, {
      method, redirect: "error", signal: AbortSignal.timeout(5000),
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    // Do not serialize headers or arbitrary server bodies into test reports.
    assert.equal(response.status, 200, `${method} ${endpoint}: HTTP ${response.status}`);
    return response.json();
  };

  try {
    await mkdir(userHome);
    await mkdir(workspaceRoot);
    assert(process.env.GOROOT && process.env.GOMODCACHE && process.env.GOCACHE,
      "Set private GOROOT, read-only GOMODCACHE and owned GOCACHE");
    const build = spawnSync(path.join(process.env.GOROOT, "bin", "go"), ["build", "-o", binary, "./cmd/consoled"], {
      cwd: repository, encoding: "utf8", timeout: 120000, killSignal: "SIGKILL",
      env: { ...environment, GOROOT: process.env.GOROOT, GOMODCACHE: process.env.GOMODCACHE,
        GOCACHE: process.env.GOCACHE, GOENV: "off", GOTOOLCHAIN: "local", GOWORK: "off",
        GOFLAGS: "-mod=readonly", GOPROXY: "off", GOSUMDB: "off", CGO_ENABLED: "0" },
    });
    buildCompletionUnverified = Boolean(build.error || build.signal);
    if (build.error) throw build.error;
    assert.equal(build.status, 0, build.stderr || `Go build terminated by ${build.signal}`);
  } catch (error) {
    try { await close(); }
    catch (cleanupError) { throw new AggregateError([error, cleanupError], "Build/setup and cleanup failed"); }
    throw error;
  }

  return {
    get origin() { return origin; },
    stop, close,
    async start() {
      assert(!interrupted && !cleaning, "Journey already closing");
      assert(!owned || owned.joined, "Previous process must be joined before restart");
      const child = spawn(binary, ["-home", home, "-listen", "127.0.0.1:0"], {
        env: environment, stdio: ["ignore", "ignore", "pipe"],
      });
      const current = { child, joined: false };
      owned = current;
      current.closed = new Promise(resolve => child.once("close", (code, signal) => {
        current.result = { code, signal };
        resolve(current.result);
      }));
      child.on("error", error => { current.error = error; });
      let output = "";
      const listening = new Promise((resolve, reject) => {
        child.once("error", reject);
        child.once("exit", (code, signal) => reject(new Error(`daemon startup exited ${code}/${signal}: ${output}`)));
        child.stderr.on("data", chunk => {
          output = (output + chunk.toString()).slice(-4096);
          const match = output.match(/consoled listening on (http:\/\/127\.0\.0\.1:\d+)/);
          if (match) resolve(match[1]);
        });
      });
      origin = await bounded(listening, 10000, "daemon listening readiness");
      token = (await readFile(path.join(home, "auth.secret"), "utf8")).trim();
      assert.equal((await fetch(origin + "/v0/workspaces", { signal: AbortSignal.timeout(5000) })).status, 401);
      await request("/v0/workspaces");
    },
    async openContext() {
      browser ??= await chromium.launch({ headless: true, env: environment, timeout: 20000 });
      const context = await browser.newContext({ viewport, locale: "en-US", serviceWorkers: "block" });
      try {
        await context.addInitScript(({ origin: allowedOrigin, credential }) => {
          if (location.origin === allowedOrigin) localStorage.setItem("bt-token", credential);
        }, { origin, credential: token });
        return context;
      } catch (error) {
        try { await context.close(); }
        catch (cleanupError) { throw new AggregateError([error, cleanupError], "Context setup and cleanup failed"); }
        throw error;
      }
    },
    async seed() {
      const workspace = await request("/v0/workspaces", "POST", { name: "Rendered fixture workspace", root: workspaceRoot });
      const accounts = [];
      for (const adapter of ["codex", "claude"]) {
        const account = await request("/v0/accounts", "POST", { label: `Fixture ${adapter} (no login)`, adapter });
        accounts.push(await request(`/v0/accounts/${account.id}/connect`, "POST", {}));
      }
      const squad = await request("/v0/squads", "POST", {
        name: "Rendered snapshot one", purpose: "Fixture only; no provider execution", origin: "custom",
        members: accounts.map((account, index) => ({ role: index ? "reviewer" : "conductor", cli: account.adapter,
          model: `fixture-${account.adapter}-v1`, account_id: account.id, maestro: index === 0 })),
      });
      const first = await request("/v0/missions", "POST", { workspace_id: workspace.id, squad_id: squad.id,
        title: "First rendered mission", brief: "First immutable rendered objective" });
      const revised = await request("/v0/squads", "POST", { ...squad, name: "Rendered snapshot two",
        members: squad.members.map(member => ({ ...member, model: `fixture-${member.cli}-v2` })) });
      const second = await request("/v0/missions", "POST", { workspace_id: workspace.id, squad_id: squad.id,
        title: "Second rendered mission", brief: "Second immutable rendered objective" });
      assert.equal(revised.id, squad.id);
      assert.deepEqual(first.squad_snapshot, squad);
      assert.deepEqual(second.squad_snapshot, revised);
      seeded = { workspace, accounts, revised, first, second };
      return seeded;
    },
    async catalog() {
      const { workspace, accounts, revised, first, second } = seeded;
      const workspaces = await request("/v0/workspaces");
      assert.deepEqual(workspaces, [workspace]);
      const listed = await request("/v0/accounts");
      assert.deepEqual(listed.map(item => item.id).sort(), accounts.map(item => item.id).sort());
      for (const account of listed) {
        const expected = accounts.find(item => item.id === account.id);
        assert.equal(account.label, expected.label);
        assert.equal(account.adapter, expected.adapter);
        assert.equal(account.vault_ref, expected.vault_ref);
        assert.equal(account.connected, true);
        assert.equal(account.authed, false);
        assert.equal(account.readiness.ready, false);
        assert.equal(account.readiness.code, "cli_missing");
      }
      const squads = await request("/v0/squads");
      assert.deepEqual(squads.find(item => item.id === revised.id), revised);
      const missions = await request(`/v0/missions?workspace=${workspace.id}`);
      assert.deepEqual(missions.map(item => item.id).sort(), [first.id, second.id].sort());
      for (const mission of [first, second]) assert.deepEqual(missions.find(item => item.id === mission.id), mission);
      assert.deepEqual(await request("/v0/sessions"), []);
      return { workspaces, squads, missions: missions.sort((a, b) => a.id.localeCompare(b.id)),
        accounts: listed.map(({ readiness: { checked_at, ...readiness }, ...account }) => ({ ...account, readiness }))
          .sort((a, b) => a.id.localeCompare(b.id)) };
    },
  };
}

// These cases use the frozen test-only host. All journey mutations below are
// rendered controls; only catalog/setup and disclosed fault controls are private.
async function selectNativeMission(page, item) {
  await page.getByRole("button", { name: "Home", exact: true }).click();
  await page.getByRole("main").getByRole("button", { name: new RegExp(item.workspace.name) })
    .filter({ has: page.getByText(item.workspace.root, { exact: true }) }).click();
  await page.getByRole("button", { name: "Missions", exact: true }).click();
  await page.getByRole("button").filter({ has: page.getByText(item.mission.id, { exact: true }) }).click();
  await expect(page.getByRole("heading", { name: item.mission.title, exact: true })).toBeVisible();
}

async function nativeMutation(page, button, endpoint, status) {
  const [response] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === endpoint &&
      ["POST", "PUT"].includes(response.request().method())),
    page.getByRole("button", { name: button, exact: true }).click(),
  ]);
  const result = await response.json();
  assert.equal(response.status(), status, `${button}: ${JSON.stringify(result)}`);
  return { body: response.request().postDataJSON(), result, status: response.status() };
}

const nativeBase = item => `/v0/missions/${item.mission.id}`;
const nativeFields = item => ({ Approval: item.approval, Delivery: item.run.delivery });
const nativeState = (fixture, item) => fixture.request(`${nativeBase(item)}/execution?workspace_id=${item.workspace.id}&approval_id=${item.approval.id}`);
const nativeStopCount = fixture => fixture.evidence.requests.filter(request => request.path.endsWith("/execution/stop")).length;

async function assertNativePlanningLayout(fixture, page, name) {
  assert.deepEqual(page.viewportSize(), { width: 1280, height: 720 });
  const control = page.getByRole("button", { name, exact: true });
  await expect(control).toBeEnabled();
  await control.scrollIntoViewIfNeeded();
  const geometry = await control.evaluate(button => {
    const card = button.closest("section");
    const body = card?.querySelector(".plan-body");
    if (!card || !body) throw new Error("Planning control has no enclosing card/body");
    const bounds = element => element.getBoundingClientRect().toJSON();
    const rect = button.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
    return { card: bounds(card), body: bounds(body), control: bounds(button),
      receivesPointer: !!hit && button.contains(hit), hit: hit?.tagName };
  });
  fixture.evidence.layout ??= [];
  fixture.evidence.layout.push({ name, viewport: page.viewportSize(), ...geometry });
  for (const [label, rect] of [["body", geometry.body], [name, geometry.control]]) {
    assert(rect.width > 0 && rect.height > 0, `${label} must have rendered area`);
    assert(rect.top >= geometry.card.top - 1 && rect.bottom <= geometry.card.bottom + 1 &&
      rect.left >= geometry.card.left - 1 && rect.right <= geometry.card.right + 1,
    `${label} must fit inside the planning card: ${JSON.stringify(geometry)}`);
  }
  assert.equal(geometry.receivesPointer, true, `${name} center must hit its control: ${JSON.stringify(geometry)}`);
}

async function approveNativeMission(fixture, page, item) {
  await selectNativeMission(page, item);
  await page.getByLabel("Plan path", { exact: true }).fill(item.source.path);
  await assertNativePlanningLayout(fixture, page, "Bind plan");
  const binding = await nativeMutation(page, "Bind plan", `${nativeBase(item)}/binding`, 200);
  assert.deepEqual(binding.body, { workspace_id: item.workspace.id, plan_path: item.source.path });
  await expect(page.getByLabel("Original plan content", { exact: true })).toHaveText(item.source.plan, { useInnerText: false });
  assert.equal(await page.getByLabel("Original plan content", { exact: true }).textContent(), item.source.plan);
  await expect(page.getByText("out.txt", { exact: true })).toBeVisible();
  await assertNativePlanningLayout(fixture, page, "Approve displayed bytes");
  const approval = await nativeMutation(page, "Approve displayed bytes", `${nativeBase(item)}/plan/approvals`, 200);
  assert.equal(approval.result.effect, "recorded");
  item.approval = approval.result.receipt;
  assert.deepEqual(approval.body, item.approval.identity);
  assert.deepEqual(approval.result.target, item.approval.identity);
  assert.equal(Buffer.from(item.approval.content, "base64").toString(), item.source.plan);
  assert.equal(item.approval.identity.binding.canonical_root, item.source.root);
  await expect(page.getByText(item.approval.id, { exact: true })).toBeVisible();
}

async function launchNativeMission(page, item, resume = false) {
  const previous = item.run;
  const admission = await nativeMutation(page, resume ? "Resume this delivery" : "Start native execution",
    `${nativeBase(item)}/execution${resume ? "/resume" : ""}`, 202);
  assert.equal(admission.result.effect, "started");
  assert.deepEqual(admission.result.target, item.approval.identity);
  assert.equal(admission.result.approval_id, item.approval.id);
  assert.match(admission.result.run_id, /^[a-f0-9]{64}$/);
  assert(admission.result.opened_digest);
  assert.deepEqual(admission.body, resume ? { target: item.approval.identity, approval_id: item.approval.id,
    delivery: previous.delivery, opened_digest: previous.opened_digest } : { target: item.approval.identity, approval_id: item.approval.id });
  if (resume) {
    assert.notEqual(admission.result.run_id, previous.run_id);
    assert.equal(admission.result.delivery, previous.delivery);
    assert.equal(admission.result.opened_digest, previous.opened_digest);
  }
  item.run = admission.result;
  return admission.result;
}

function assertStopReference(observation, reference) {
  assert.deepEqual(observation.body, reference);
  for (const key of ["target", "approval_id", "delivery", "run_id"]) assert.deepEqual(observation.result[key], reference[key]);
}
function assertNativePending(observation, reference) {
  assertStopReference(observation, reference);
  assert.equal(observation.status, 202);
  assert.equal(observation.result.effect, "pending");
  assert.equal(observation.result.finalized, false);
  assert.equal(observation.result.durable, false);
}
async function observeNativeStop(page, item, reference, finalStatus) {
  let final;
  await expect.poll(async () => {
    // Each observation is an explicit rendered action, permitted only by pending.
    const [response] = await Promise.all([
      page.waitForResponse(response => new URL(response.url()).pathname === `${nativeBase(item)}/execution/stop` && response.request().method() === "POST"),
      page.getByRole("button", { name: "Observe this Stop again", exact: true }).click(),
    ]);
    final = { body: response.request().postDataJSON(), result: await response.json(), status: response.status() };
    assertStopReference(final, reference);
    if (final.status === 202) assertNativePending(final, reference);
    else assert.equal(final.status, finalStatus);
    return final.status;
  }, { timeout: 10000, intervals: [50, 100, 250] }).toBe(finalStatus);
  return final.result;
}
async function nativeJournal(fixture, item) { return fixture.control("journal", nativeFields(item)); }
async function assertNativeShip(fixture, page, item) {
  let proof;
  await expect.poll(async () => { proof = await fixture.control("ship", nativeFields(item)); return proof.ready; },
    { timeout: 30000, intervals: [100, 250, 500] }).toBe(true);
  let state;
  await expect.poll(async () => {
    const observed = await fixture.inspectExecution(`${nativeBase(item)}/execution?workspace_id=${item.workspace.id}&approval_id=${item.approval.id}`);
    if (observed.status === 503) {
      assert.deepEqual(observed.result, { error: "plan_io", message: "artifact could not be read" });
      return false;
    }
    assert.equal(observed.status, 200);
    state = observed.result;
    return !state.active;
  }).toBe(true);
  assert.equal(state.run_id || "", "");
  assert.equal(state.watch.header.state, "done");
  assert.equal(state.delivery, item.run.delivery);
  assert.equal(state.opened_digest, item.run.opened_digest);
  assert.equal(state.gate.required, true);
  assert.equal(state.gate.cleared, true);
  assert(state.gate.evidence_digest);
  const records = await nativeJournal(fixture, item);
  const openings = records.filter(record => record.kind === "delivery_opened");
  assert.equal(openings.length, 1);
  assert.equal(openings[0].digest, item.run.opened_digest);
  await page.getByRole("button", { name: "Inspect mission state", exact: true }).click();
  await expect(page.getByLabel("Mission execution status", { exact: true })).toHaveText("Completed");
  await expect(page.getByText(`${item.run.delivery} · ${item.run.opened_digest}`, { exact: true })).toBeVisible();
}

for (const provider of ["codex", "claude"]) {
  base(`rendered native ${provider}: two missions, exact Stop, fresh-origin restart and both SHIP`, async ({ viewport }, testInfo) => {
    const fixture = await createNativeStopFixture(provider, viewport, testInfo);
    let failed = true;
    try {
      await fixture.start();
      const [a, b] = await fixture.seed();
      let page = await fixture.openPage();
      await approveNativeMission(fixture, page, a);
      await approveNativeMission(fixture, page, b);
      for (const key of ["id"]) assert.notEqual(a.approval[key], b.approval[key]);
      assert.notEqual(a.source.root, b.source.root);
      await selectNativeMission(page, a);
      const first = await launchNativeMission(page, a);
      let waiting;
      await expect.poll(async () => {
        waiting = await nativeState(fixture, a);
        return !waiting.active && Boolean(waiting.watch?.pending_question);
      }).toBe(true);
      const question = waiting.watch.pending_question;
      assert.equal(question.prompt, "exact native question?");
      assert.equal(question.target.mission_id, a.mission.id);
      assert.equal(question.target.workspace_id, a.workspace.id);
      assert.equal(question.target.delivery, first.delivery);
      assert.equal(question.target.opened_digest, first.opened_digest);
      assert(question.target.task && question.target.question_id && question.target.execution > 0);
      await expect(page.getByLabel("Mission execution status", { exact: true })).toHaveText("Waiting");
      await expect(page.getByText(question.prompt, { exact: true })).toBeVisible();
      await expect(page.getByText(`${question.target.task} · ${question.target.execution} · ${question.target.question_id} · ${question.target.opened_digest}`, { exact: true })).toBeVisible();
      const before = await nativeJournal(fixture, a);
      await page.getByLabel("Answer this question", { exact: true }).fill("exact native answer");
      const answered = await nativeMutation(page, "Send reply", "/v0/watch/answer", 200);
      assert.deepEqual(answered.body, { ...question.target, text: "exact native answer" });
      assert.equal(answered.result.effect, "recorded");
      assert.deepEqual(answered.result.target, question.target);
      assert.equal(answered.result.delivery, first.delivery);
      await expect(page.getByText("Answer recorded. Resume is a separate action.", { exact: true })).toBeVisible();
      const idle = await nativeState(fixture, a);
      assert.equal(idle.active, false);
      assert.equal(idle.run_id || "", "");
      const after = await nativeJournal(fixture, a);
      assert.equal(after.length, before.length + 1);
      assert.equal(after.at(-1).kind, "answer_recorded");
      await assert.rejects(readFile(path.join(a.source.root, ".git/public-flow/first.birth")), { code: "ENOENT" });
      assert.equal(fixture.evidence.requests.filter(request => request.path.endsWith("/execution/resume")).length, 0);
      await launchNativeMission(page, a, true);
      const target = await fixture.control("tree", { ...nativeFields(a), Phase: "first" });
      await selectNativeMission(page, b);
      await launchNativeMission(page, b);
      const sibling = await fixture.control("tree", { ...nativeFields(b), Phase: "first" });
      const foreign = await fixture.control("foreign");
      assert.notEqual(a.run.delivery, b.run.delivery);
      assert.notEqual(a.run.run_id, b.run.run_id);
      await selectNativeMission(page, a);
      await page.getByRole("button", { name: "Stop native attempt", exact: true }).click();
      const reference = { target: a.approval.identity, approval_id: a.approval.id, delivery: a.run.delivery, run_id: a.run.run_id };
      // Confirm the onscreen frozen selection; persistence is qualified after dispatch.
      const count = nativeStopCount(fixture);
      await expect(page.getByText(`${reference.delivery} · ${reference.run_id}`, { exact: true })).toBeVisible();
      assert.equal(nativeStopCount(fixture), count);
      const wrong = await fixture.request(`${nativeBase(a)}/execution/stop`, "POST", { ...reference, run_id: b.run.run_id }, 400);
      assert.equal(wrong.effect, "rejected");
      await fixture.control("alive", { Births: [...target, ...sibling, foreign] });
      await expect(page.getByRole("button", { name: "Confirm Stop", exact: true })).toBeEnabled();
      await fixture.control("hold", nativeFields(a));
      try {
        const pending = await nativeMutation(page, "Confirm Stop", `${nativeBase(a)}/execution/stop`, 202);
        assertNativePending(pending, reference);
        await expect(page.getByText("Native Stop pending finalization.", { exact: true })).toBeVisible();
        const observed = await nativeMutation(page, "Observe this Stop again", `${nativeBase(a)}/execution/stop`, 202);
        assertNativePending(observed, reference);
        assert.deepEqual(observed.result, pending.result);
        await fixture.control("retired", { Births: target });
        await expect.poll(async () => (await fixture.control("stacks")).split("\n\ngoroutine ")
          .some(stack => stack.includes("journal.lockJournalContext") && stack.includes("/core/loop.")),
        { timeout: 3000, intervals: [10, 25, 50] }).toBe(true);
      } finally { await fixture.control("unlock", nativeFields(a)); }
      // Reload/navigation observe the cached pending response after releasing the
      // real journal barrier; neither may spend the native finalization budget.
      const heldCount = nativeStopCount(fixture);
      const heldMutations = fixture.evidence.requests.length;
      await selectNativeMission(page, b);
      await expect(page.getByText(`${b.run.delivery} · ${b.run.opened_digest}`, { exact: true })).toBeVisible();
      await expect(page.getByText(`${reference.delivery} · ${reference.run_id}`, { exact: true })).toHaveCount(0);
      await selectNativeMission(page, a);
      await expect(page.getByText(`${reference.delivery} · ${reference.run_id}`, { exact: true })).toBeVisible();
      await expect(page.getByText("Native Stop pending finalization.", { exact: true })).toBeVisible();
      await page.reload({ waitUntil: "domcontentloaded" });
      await selectNativeMission(page, a);
      await expect(page.getByText(`${reference.delivery} · ${reference.run_id}`, { exact: true })).toBeVisible();
      await expect(page.getByText("Native Stop pending finalization.", { exact: true })).toBeVisible();
      assert.equal(nativeStopCount(fixture), heldCount);
      assert.equal(fixture.evidence.requests.length, heldMutations);
      await fixture.control("alive", { Births: [...sibling, foreign] });
      const completed = await observeNativeStop(page, a, reference, 200);
      assert.equal(completed.effect, "completed");
      assert.equal(completed.finalized, true);
      assert.equal(completed.durable, true);
      assert.equal(completed.core_state, "canceled");
      assert.equal(completed.state.active, false);
      assert.equal(completed.state.run_id || "", "");
      assert.equal(completed.state.watch.header.state, "canceled");
      await expect(page.getByText("Native Stop finalized durably.", { exact: true })).toBeVisible();
      await expect(page.getByText("Core state: canceled", { exact: true })).toBeVisible();
      await selectNativeMission(page, b);
      await fixture.control("release", { ...nativeFields(b), Phase: "first" });
      await assertNativeShip(fixture, page, b);
      await fixture.control("retired", { Births: sibling });
      const journals = [await nativeJournal(fixture, a), await nativeJournal(fixture, b)];
      const restartMutations = fixture.evidence.requests.length;
      await fixture.closePage(page);
      await fixture.stop();
      await fixture.start();
      page = await fixture.openPage();
      for (const [index, item] of [a, b].entries()) {
        assert.deepEqual(await nativeJournal(fixture, item), journals[index]);
        assert.deepEqual(await fixture.request(`${nativeBase(item)}/plan/approvals/${item.approval.id}?workspace_id=${item.workspace.id}`), item.approval);
        await selectNativeMission(page, item);
        await expect(page.getByText(item.approval.id, { exact: true })).toBeVisible();
        assert.equal(await page.getByLabel("Original plan content", { exact: true }).textContent(), item.source.plan);
        await expect(page.getByText(`${item.run.delivery} · ${item.run.opened_digest}`, { exact: true })).toBeVisible();
      }
      await assertNativeShip(fixture, page, b);
      assert.equal(fixture.evidence.requests.length, restartMutations, "fresh origin must not replay mutations");
      const canceled = await nativeState(fixture, a);
      assert.equal(canceled.active, false);
      assert.equal(canceled.run_id || "", "");
      assert.equal(canceled.watch.header.state, "canceled");
      assert.equal(canceled.delivery, a.run.delivery);
      assert.equal(canceled.opened_digest, a.run.opened_digest);
      assert.equal(canceled.gate.required, true);
      assert.equal(canceled.gate.cleared, false);
      assert.equal((await fixture.request(`${nativeBase(a)}/execution/stop`, "POST", reference, 400)).effect, "rejected");
      await selectNativeMission(page, a);
      await expect(page.getByLabel("Mission execution status", { exact: true })).toHaveText("Review blocked");
      await expect(page.getByRole("button", { name: "Resume this delivery", exact: true })).toBeEnabled();
      await fixture.control("phase", { ...nativeFields(a), Phase: "replacement" });
      await launchNativeMission(page, a, true);
      const replacement = await fixture.control("tree", { ...nativeFields(a), Phase: "replacement" });
      const replacementForeign = await fixture.control("foreign");
      assert.equal((await fixture.request(`${nativeBase(a)}/execution/stop`, "POST", reference, 400)).effect, "rejected");
      const running = await nativeState(fixture, a);
      assert.equal(running.active, true);
      assert.equal(running.run_id, a.run.run_id);
      await fixture.control("alive", { Births: [...replacement, replacementForeign] });
      await fixture.control("release", { ...nativeFields(a), Phase: "replacement" });
      await assertNativeShip(fixture, page, a);
      await fixture.control("retired", { Births: replacement });
      assert.equal(fixture.evidence.requests.filter(request => request.path.endsWith("/plan/approvals")).length, 2);
      assert(fixture.evidence.assets.some(asset => asset.path.endsWith(".js") && asset.status === 200));
      failed = false;
    } finally { await fixture.close(failed); }
  });
}

base("rendered native Stop: disposable same-inode journal fault is HTTP 503 uncertain", async ({ viewport }, testInfo) => {
  const fixture = await createNativeStopFixture("codex", viewport, testInfo);
  let failed = true;
  try {
    await fixture.start();
    const [item] = await fixture.seed(false, 1);
    const page = await fixture.openPage();
    await approveNativeMission(fixture, page, item);
    await launchNativeMission(page, item);
    const target = await fixture.control("tree", { ...nativeFields(item), Phase: "first" });
    const reference = { target: item.approval.identity, approval_id: item.approval.id, delivery: item.run.delivery, run_id: item.run.run_id };
    await page.getByRole("button", { name: "Stop native attempt", exact: true }).click();
    await expect(page.getByRole("button", { name: "Confirm Stop", exact: true })).toBeEnabled();
    await fixture.control("hold", nativeFields(item));
    let released = false;
    try {
      assertNativePending(await nativeMutation(page, "Confirm Stop", `${nativeBase(item)}/execution/stop`, 202), reference);
      await expect(page.getByText("Native Stop pending finalization.", { exact: true })).toBeVisible();
      await fixture.control("retired", { Births: target });
      await expect.poll(async () => (await fixture.control("stacks")).split("\n\ngoroutine ")
        .some(stack => stack.includes("journal.lockJournalContext") && stack.includes("/core/loop.")),
      { timeout: 3000, intervals: [10, 25, 50] }).toBe(true);
      const fault = await fixture.control("corrupt", { ...nativeFields(item), Births: target });
      released = true;
      assert.equal(fault.same_inode, true);
    } finally { if (!released) await fixture.control("unlock", nativeFields(item)); }
    const result = await observeNativeStop(page, item, reference, 503);
    assert.equal(result.effect, "uncertain");
    assert.equal(result.finalized, true);
    assert.equal(result.durable, false);
    assert.equal(result.core_state || "", "");
    assert(result.error);
    assert.match(result.causes.join("\n"), /artifact.*invalid|invalid.*artifact/);
    assert.match(result.causes.join("\n"), /context canceled/);
    await expect(page.getByText("Native Stop outcome uncertain. Inspect this attempt.", { exact: true })).toBeVisible();
    for (const cause of result.causes) await expect(page.getByText(cause, { exact: true })).toBeVisible();
    await expect(page.getByText(`${result.error.code}: ${result.error.message}`, { exact: true })).toBeVisible();
    await expect(page.getByText("Native Stop finalized durably.", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Observe this Stop again", exact: true })).toHaveCount(0);
    const count = nativeStopCount(fixture);
    await page.reload({ waitUntil: "domcontentloaded" });
    await selectNativeMission(page, item);
    await expect(page.getByText("Native Stop outcome uncertain. Inspect this attempt.", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Confirm Stop", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Stop native attempt", exact: true })).toHaveCount(0);
    await expect(page.getByLabel("Mission execution status", { exact: true })).not.toHaveText("Completed");
    assert.equal(nativeStopCount(fixture), count);
    failed = false;
  } finally { await fixture.close(failed); }
});

base("native browser observer finishes real response reads before context close", async ({ viewport }, testInfo) => {
  let enter, release;
  const entered = new Promise(resolve => { enter = resolve; });
  const barrier = new Promise(resolve => { release = resolve; });
  let armed = false;
  const fixture = await createNativeStopFixture("codex", viewport, testInfo, async response => {
    if (armed && response.request().method() === "GET" && new URL(response.url()).pathname === "/v0/missions") {
      armed = false;
      enter();
      // Only the evidence reader waits; the real HTTP response and page stay untouched.
      await barrier;
    }
  });
  const errors = [];
  let closing;
  try {
    await fixture.start();
    await fixture.seed(false, 1);
    const page = await fixture.openPage();
    armed = true;
    const fetched = page.evaluate(async () => {
      const response = await fetch("/v0/missions", { headers: { Authorization: `Bearer ${localStorage.getItem("bt-token")}` } });
      return { status: response.status, body: await response.json() };
    });
    await bounded(entered, 10000, "held real response observer");
    const result = await fetched;
    assert.equal(result.status, 200);
    assert.equal(result.body.length, 1);
    closing = fixture.closePage(page);
    const status = await page.evaluate(async () => {
      const response = await fetch("/v0/capabilities", { headers: { Authorization: `Bearer ${localStorage.getItem("bt-token")}` } });
      await response.arrayBuffer();
      return response.status;
    });
    assert.equal(status, 200, "context must remain usable while its evidence reader is held");
    release();
    await closing;
    assert.equal(page.isClosed(), true);
  } catch (error) {
    errors.push(error);
  } finally {
    release();
    if (closing) try { await closing; } catch (error) { errors.push(error); }
    try { await fixture.close(errors.length > 0); } catch (error) { errors.push(error); }
  }
  if (errors.length) throw new AggregateError(errors, "response observer/context lifecycle failed");
});
