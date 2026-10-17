import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { createWriteStream } from "node:fs";
import { chmod, mkdir, mkdtemp, readFile, realpath, rm, stat, writeFile } from "node:fs/promises";
import { createInterface } from "node:readline";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, expect } from "@playwright/test";

const repository = fileURLToPath(new URL("../../", import.meta.url));
const hash = bytes => createHash("sha256").update(bytes).digest("hex");
export async function bounded(promise, milliseconds, label) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`${label} exceeded ${milliseconds}ms`)), milliseconds);
    })]);
  } finally { clearTimeout(timer); }
}

async function sourceReceipt() {
  const listed = spawnSync("/usr/bin/git", ["ls-files", "-z"], { cwd: repository });
  assert.equal(listed.status, 0);
  const names = listed.stdout.toString().split("\0").filter(Boolean);
  const digest = createHash("sha256");
  const files = {};
  for (const name of names) {
    const bytes = await readFile(path.join(repository, name));
    digest.update(`${name}\0${bytes.length}\0`).update(bytes);
    files[name] = hash(bytes);
  }
  const hostDigest = digest.digest("hex");
  const fixture = "web/tests/native-stop-host-fixture.mjs";
  files[fixture] = hash(await readFile(path.join(repository, fixture)));
  return { hostDigest, files };
}

// Consumer: create -> start -> seed -> openPage; stop joins before start reuses
// Store/roots. Every caller must close(failed) in finally. No credentials enter
// the evidence stream. Pipe controls are fixture evidence, never rendered proof.
export async function createNativeStopFixture(provider, viewport, testInfo, beforeResponseObservation) {
  const temporaryRoot = await realpath(os.tmpdir());
  for (let dir = temporaryRoot; ; dir = path.dirname(dir)) {
    try { await realpath(path.join(dir, ".git")); throw new Error("Temporary root must be outside Git"); }
    catch (error) { if (error.code !== "ENOENT") throw error; }
    if (dir === path.dirname(dir)) break;
  }
  const temporary = await mkdtemp(path.join(temporaryRoot, "console-native-browser-"));
  await chmod(temporary, 0o700);
  const proof = testInfo.outputPath("native-stop-evidence.json");
  await mkdir(path.dirname(proof), { recursive: true, mode: 0o700 });
  const evidence = { provider, temporary, controls: [], http: [], requests: [], assets: [], joins: [], interruptedReads: [] };
  const initial = await sourceReceipt();
  evidence.source = initial;
  const environment = {
    HOME: path.join(temporary, "user"), PATH: `${process.env.GOROOT}/bin:/usr/bin:/bin`,
    TMPDIR: temporary, TMP: temporary, TEMP: temporary, BATUTA_LANG: "en", LANG: "en_US.UTF-8",
    GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1",
    GOENV: "off", GOTOOLCHAIN: "local", GOWORK: "off", GOFLAGS: "-mod=readonly",
    GOPROXY: "off", GOSUMDB: "off", CGO_ENABLED: "1", GOROOT: process.env.GOROOT,
    GOMODCACHE: process.env.GOMODCACHE, GOCACHE: process.env.GOCACHE,
    XDG_CONFIG_HOME: path.join(temporary, "user/config"), XDG_DATA_HOME: path.join(temporary, "user/data"),
    XDG_STATE_HOME: path.join(temporary, "user/state"), XDG_CACHE_HOME: path.join(temporary, "user/cache"),
  };
  let current, browser, token, origin, cfg, cleaning;
  const contexts = new Set();
  const readers = new Set();
  const observers = new Map();
  const readErrors = [];
  let unjoined = false;
  let interrupted;
  const guard = async label => {
    assert.deepEqual(await sourceReceipt(), initial, `tracked source changed: ${label}`);
  };
  function owned(command, args, env, label, extra = []) {
    const log = createWriteStream(path.join(temporary, `${label}.log`), { mode: 0o600 });
    const child = spawn(command, args, { cwd: path.join(repository, "internal/tui"), env,
      stdio: ["ignore", "pipe", "pipe", ...extra] });
    const item = { child, log, label, joined: false };
    child.stdout.pipe(log, { end: false });
    child.stderr.pipe(log, { end: false });
    item.closed = new Promise(resolve => {
      child.on("error", error => { item.error = error; });
      child.once("close", (code, signal) => {
        item.result = { code, signal };
        log.end(() => resolve(item.result));
      });
    });
    return item;
  }
  async function join(item, timeout) {
    const result = await bounded(item.closed, timeout, `${item.label} join`);
    item.joined = true;
    evidence.joins.push({ label: item.label, pid: item.child.pid, ...result });
    assert(!item.error && result.code === 0 && result.signal === null,
      `${item.label} failed; private log retained at ${temporary}`);
  }
  async function runBuild(command, args, env, label) {
    await guard(`before ${label}`);
    const item = owned(command, args, env, label);
    try { await join(item, 120000); }
    catch (error) {
      if (!item.joined) {
        unjoined = true;
        item.child.kill("SIGKILL");
        await bounded(item.closed, 10000, `${label} forced join`);
      }
      throw error;
    } finally { await guard(`after ${label}`); }
  }
  async function nextEvent(kind) {
    const result = await bounded(current.events.next(), 10000, `host event ${kind}`);
    assert(!result.done, `host events ended before ${kind}; logs: ${temporary}`);
    const event = JSON.parse(result.value);
    assert.equal(event.Kind, kind);
    return event;
  }
  const control = async (Op, fields = {}) => {
    assert(current && !current.result, "host must be live");
    current.child.stdio[3].write(JSON.stringify({ Op, ...fields }) + "\n");
    const event = await nextEvent(Op === "authenticate" ? "authenticated" : Op);
    evidence.controls.push({ Op, ...fields, event });
    return Op === "stacks" ? event.Stacks : event.Data;
  };
  const stop = async () => {
    if (!current || current.joined) return;
    const item = current;
    item.child.stdio[3].end();
    try { await join(item, 15000); }
    catch (error) {
      if (!item.joined) {
        unjoined = true;
        item.child.kill("SIGKILL");
        await bounded(item.closed, 5000, "forced owned host join");
      }
      throw error;
    } finally {
      item.lines.close();
      item.child.stdio[4].destroy();
      await guard("host drain");
    }
  };
  async function closeContext(context) {
    const observer = observers.get(context);
    if (observer) observer.page.off("response", observer.listener);
    const errors = [];
    try {
      await bounded(Promise.all([...(observer?.readers || [])]), 10000, "response readers before context close");
      evidence.joins.push({ label: "response readers before context close", joined: true });
    } catch (error) { errors.push(error); }
    try {
      await bounded(context.close(), 10000, "context close");
      contexts.delete(context);
      observers.delete(context);
    } catch (error) { errors.push(error); }
    if (errors.length) throw new AggregateError(errors, "response reader/context join failed");
  }
  const close = failed => cleaning ??= (async () => {
    const errors = [];
    for (const context of [...contexts]) {
      try { await closeContext(context); } catch (error) { errors.push(error); }
    }
    contexts.clear();
    if (browser) {
      try { await bounded(browser.close(), 10000, "browser close"); } catch (error) { errors.push(error); }
    }
    try { await bounded(Promise.all([...readers]), 10000, "response reader joins"); } catch (error) { errors.push(error); }
    errors.push(...readErrors);
    try { await stop(); } catch (error) { errors.push(error); }
    try { await guard("fixture close"); } catch (error) { errors.push(error); }
    if (interrupted) errors.push(new Error(`interrupted: ${interrupted}`));
    evidence.retained = Boolean(failed || errors.length || unjoined);
    await writeFile(proof, JSON.stringify(evidence, null, 2), { mode: 0o600 });
    if (!evidence.retained) await rm(temporary, { recursive: true, force: true });
    process.off("SIGINT", onSignal);
    process.off("SIGTERM", onSignal);
    token = undefined;
    if (errors.length) throw new AggregateError(errors, `fixture cleanup failed; retained ${temporary}`);
  })();
  const onSignal = signal => { interrupted = signal; void close(true).catch(error => readErrors.push(error)); };
  process.on("SIGINT", onSignal);
  process.on("SIGTERM", onSignal);
  const exchange = async (endpoint, method = "GET", body) => {
    const response = await fetch(origin + endpoint, { method, redirect: "error", signal: AbortSignal.timeout(10000),
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body) });
    const result = await response.json();
    evidence.http.push({ source: "fixture HTTP", endpoint, method, body, status: response.status, result });
    return { status: response.status, result };
  };
  const request = async (endpoint, method = "GET", body, status = 200) => {
    const observed = await exchange(endpoint, method, body);
    assert.equal(observed.status, status, `${method} ${endpoint}`);
    return observed.result;
  };
  const inspectExecution = async endpoint => {
    assert(/^\/v0\/missions\/[^/?]+\/execution\?/.test(endpoint), "execution-state GET required");
    return exchange(endpoint);
  };
  try {
    assert(environment.GOROOT && environment.GOMODCACHE && environment.GOCACHE, "pinned Go environment required");
    await mkdir(environment.HOME, { mode: 0o700 });
    const binary = path.join(temporary, "host.test");
    await runBuild(path.join(environment.GOROOT, "bin/go"), ["test", "-c", "-o", binary, "."], environment, "compile");
    const prepared = path.join(temporary, "prepared");
    await runBuild(binary, ["-test.run=^$", "-test.timeout=3m"], { ...environment,
      BATUTA_TEST_PUBLIC_FLOW_PREPARE: prepared, BATUTA_TEST_PUBLIC_FLOW_PROVIDER: provider }, "prepare");
    cfg = JSON.parse(await readFile(path.join(prepared, "config.json"), "utf8"));
    assert.equal(cfg.SourceDigest, initial.hostDigest);
    assert.equal((await readFile(path.join(prepared, "host.sha256"), "utf8")).trim(), hash(await readFile(binary)));
    assert.equal(cfg.CoreSHA256, hash(await readFile(cfg.Core)));
    assert.equal(cfg.HelperSHA256, hash(await readFile(cfg.Helper)));
    evidence.provenance = cfg;
    evidence.hostSHA256 = hash(await readFile(binary));
    evidence.coreModule = JSON.parse(await readFile(path.join(prepared, "core-module.json"), "utf8"));
    let generation = 0;
    return {
      get origin() { return origin; }, evidence, control, request, inspectExecution, stop, close,
      async start() {
        assert(!current || current.joined, "previous host must be joined");
        await guard("host launch");
        current = owned(binary, ["-test.run=^$", "-test.timeout=3m"], { ...environment,
          HOME: cfg.Home, PATH: `${cfg.Bin}:/usr/bin:/bin`, BATUTA_SKILLS: path.join(cfg.Skills, "batuta"),
          BATUTA_TEST_PUBLIC_FLOW_CONFIG: path.join(prepared, "config.json") }, `host-${++generation}`, ["pipe", "pipe"]);
        current.lines = createInterface({ input: current.child.stdio[4] });
        current.events = current.lines[Symbol.asyncIterator]();
        const ready = await nextEvent("ready");
        assert.equal(ready.PID, current.child.pid);
        assert.deepEqual(ready.Config, cfg);
        const previous = origin;
        origin = `http://${ready.Address}`;
        if (previous) assert.notEqual(origin, previous, "recreation must establish a new browser origin");
        evidence.controls.push({ Op: "ready", event: ready });
        const auth = path.join(cfg.Home, "auth.secret");
        assert.equal((await stat(auth)).mode & 0o777, 0o600);
        token = (await readFile(auth, "utf8")).trim();
        const unauthenticated = await fetch(origin + "/v0/capabilities", { signal: AbortSignal.timeout(5000) });
        assert.equal(unauthenticated.status, 401);
        await unauthenticated.arrayBuffer();
        evidence.http.push({ endpoint: "/v0/capabilities", authorized: false, status: 401 });
        await request("/v0/capabilities");
      },
      async seed(ask = true, count = 2) {
        const account = await request("/v0/accounts", "POST", { label: `Owned ${provider}`, adapter: provider });
        await control("authenticate", { AccountID: account.id });
        const connected = await request(`/v0/accounts/${account.id}/connect`, "POST", {});
        assert.equal(connected.session_cap, 2);
        assert.equal(connected.connected, true);
        const squad = await request("/v0/squads", "POST", { name: `Owned ${provider}`, members: [{ role: "lead", cli: provider,
          model: provider === "codex" ? "qa-native" : "sonnet", account_id: account.id, skills: ["batuta-loop", "batuta-review"], maestro: true }] });
        const missions = [];
        for (let index = 0; index < count; index++) {
          const source = await control("workspace", { Ask: ask && index === 0 });
          const workspace = await request("/v0/workspaces", "POST", { name: `Rendered ${provider} ${index}`, root: source.root });
          const mission = await request("/v0/missions", "POST", { workspace_id: workspace.id, squad_id: squad.id,
            title: `Rendered native mission ${index}`, brief: `Only approved out.txt for mission ${index}` });
          missions.push({ source, workspace, mission });
        }
        return missions;
      },
      async openPage() {
        browser ??= await chromium.launch({ headless: true, env: environment, timeout: 20000 });
        const context = await browser.newContext({ viewport, locale: "en-US", serviceWorkers: "block" });
        contexts.add(context);
        await context.addInitScript(({ allowedOrigin, credential }) => {
          if (location.origin === allowedOrigin) localStorage.setItem("bt-token", credential);
        }, { allowedOrigin: origin, credential: token });
        const page = await context.newPage();
        page.setDefaultTimeout(10000);
        page.setDefaultNavigationTimeout(10000);
        page.on("request", req => {
          const url = new URL(req.url());
          if (url.origin === origin && ["POST", "PUT", "DELETE"].includes(req.method())) {
            evidence.requests.push({ path: url.pathname, method: req.method(), body: req.postDataJSON() });
          }
        });
        const contextReaders = new Set();
        const observeResponse = response => {
          const url = new URL(response.url());
          if (url.origin !== origin) return;
          const observe = async () => {
            if (url.pathname.startsWith("/assets/")) {
              const bytes = await response.body();
              const tracked = await readFile(path.join(repository, "internal/webui/dist", url.pathname));
              assert.equal(hash(bytes), hash(tracked), "rendered asset differs from tracked embedded bytes");
              evidence.assets.push({ path: url.pathname, status: response.status(), sha256: hash(bytes) });
            } else if (url.pathname.startsWith("/v0/missions") || url.pathname === "/v0/watch/answer") {
              if (beforeResponseObservation) await beforeResponseObservation(response);
              const request = response.request();
              const record = { source: "Chromium", endpoint: url.pathname + url.search,
                method: request.method(), body: request.postDataJSON(), status: response.status() };
              // Background GET bodies can disappear on document navigation. Required
              // state reads are asserted separately; mutation bodies remain mandatory.
              if (request.method() === "GET") evidence.http.push({ ...record, bodyCaptured: false });
              else evidence.http.push({ ...record, bodyCaptured: true, result: await response.json() });
            }
          };
          const reader = observe().catch(error => {
            const request = response.request();
            if (request.method() === "GET" && url.pathname.startsWith("/v0/") && request.failure()) {
              evidence.interruptedReads.push({ path: url.pathname, failure: request.failure(), error: error.message });
            } else readErrors.push(error);
          }).finally(() => {
            readers.delete(reader);
            contextReaders.delete(reader);
          });
          readers.add(reader);
          contextReaders.add(reader);
        };
        page.on("response", observeResponse);
        observers.set(context, { page, listener: observeResponse, readers: contextReaders });
        await page.goto(origin, { waitUntil: "domcontentloaded" });
        await expect(page.getByRole("button", { name: "Missions", exact: true })).toBeVisible();
        return page;
      },
      async closePage(page) { await closeContext(page.context()); },
    };
  } catch (error) {
    try { await close(true); } catch (cleanupError) { throw new AggregateError([error, cleanupError], "setup and cleanup failed"); }
    throw error;
  }
}
