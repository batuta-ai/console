import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, realpath, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test as base, expect, chromium } from "@playwright/test";

const test = base.extend({
  journey: [async ({}, use) => {
    const fixture = await createJourney();
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
      await page.getByRole("button", { name: new RegExp(workspace.name) })
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
          await page.getByRole("button", { name: new RegExp(member.role) }).click();
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

async function createJourney() {
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
    assert.equal(build.status, 0, build.error?.message || build.stderr);
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
      const context = await browser.newContext({ locale: "en-US", serviceWorkers: "block" });
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
