import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = fileURLToPath(new URL("../../", import.meta.url));
const temporary = await mkdtemp(path.join(os.tmpdir(), "console-journey-"));
let daemon;
let exited;
let probe;
const stopDaemon = async () => {
  if (!daemon) return;
  let deadline;
  let forced = false;
  if (daemon.exitCode === null && daemon.signalCode === null) {
    daemon.kill("SIGTERM");
    deadline = setTimeout(() => {
      forced = true;
      daemon.kill("SIGKILL");
    }, 5000);
  }
  try {
    const { code, signal } = await exited;
    assert(!forced && code === 0 && signal === null,
      `daemon stop was not graceful: exit=${code} signal=${signal} forced=${forced}`);
  } finally { clearTimeout(deadline); }
};
let cleaning;
let interruptedSignal = false;
const cleanup = () => cleaning ??= (async () => {
  try {
    if (probe?.listening) await new Promise((resolve, reject) => probe.close(error => error ? reject(error) : resolve()));
  } finally {
    try { await stopDaemon(); }
    finally { await rm(temporary, { recursive: true, force: true }); }
  }
})();
const interrupted = () => {
  interruptedSignal = true;
  void cleanup().then(() => process.exit(130), error => { console.error(error); process.exit(1); });
};
process.once("SIGINT", interrupted);
process.once("SIGTERM", interrupted);
let failure;
try {
  // Reserve an available loopback port; consoled does not expose its assigned port for :0.
  probe = net.createServer();
  probe.listen(0, "127.0.0.1");
  await once(probe, "listening");
  const address = probe.address();
  assert(address && typeof address === "object");
  const port = address.port;
  await new Promise((resolve, reject) => probe.close(error => error ? reject(error) : resolve()));
  const binary = path.join(temporary, "consoled");
  const build = spawnSync("go", ["build", "-o", binary, "./cmd/consoled"], { cwd: repository, encoding: "utf8", timeout: 180000, killSignal: "SIGKILL" });
  assert.equal(build.status, 0, build.error?.message || build.stderr);
  const home = path.join(temporary, "runtime");
  const userHome = path.join(temporary, "user");
  const workspaceRoot = path.join(temporary, "workspace");
  await mkdir(userHome);
  await mkdir(workspaceRoot);
  // Only these variables reach the daemon. No inherited provider credentials or personal config.
  const environment = {
    HOME: userHome, PATH: "", TMPDIR: temporary,
    XDG_CONFIG_HOME: path.join(userHome, ".config"),
    XDG_DATA_HOME: path.join(userHome, ".local", "share"),
    XDG_STATE_HOME: path.join(userHome, ".local", "state"),
  };
  const startDaemon = async () => {
    assert.equal(interruptedSignal, false);
    daemon = spawn(binary, ["-home", home, "-listen", `127.0.0.1:${port}`], { env: environment, stdio: ["ignore", "ignore", "pipe"] });
    exited = new Promise(resolve => daemon.once("close", (code, signal) => resolve({ code, signal })));
    let log = "";
    await new Promise((resolve, reject) => {
      const deadline = setTimeout(() => finish(new Error(`daemon startup timeout: ${log}`)), 10000);
      const finish = (error) => {
        clearTimeout(deadline);
        daemon.off("error", fail);
        daemon.off("exit", failExit);
        error ? reject(error) : resolve();
      };
      const fail = error => finish(error);
      const failExit = code => finish(new Error(`daemon exited ${code}: ${log}`));
      daemon.once("error", fail);
      daemon.once("exit", failExit);
      daemon.stderr.on("data", chunk => {
        log += chunk;
        if (log.includes("consoled listening on")) finish();
      });
    });
  };
  await startDaemon();
  const token = await readFile(path.join(home, "auth.secret"), "utf8");
  const base = `http://127.0.0.1:${port}`;
  const request = async (endpoint, method = "GET", body, expectedStatus = 200) => {
    const response = await fetch(base + endpoint, {
      method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000),
    });
    assert.equal(response.status, expectedStatus, `${endpoint}: ${await response.clone().text()}`);
    return response.json();
  };
  assert.equal((await fetch(base + "/v0/workspaces", { signal: AbortSignal.timeout(5000) })).status, 401);
  assert.deepEqual(await request("/v0/workspaces"), []);
  const workspace = await request("/v0/workspaces", "POST", { name: "Synthetic workspace", root: workspaceRoot });
  assert.equal(workspace.name, "Synthetic workspace");
  assert.equal(workspace.root, workspaceRoot);
  assert.equal(workspace.profile, false);
  assert.deepEqual(workspace.setup, { ready: false, state: "missing", code: "profile_missing", configuration_code: "profile_missing" });
  assert.deepEqual((await request("/v0/workspaces")).map(item => item.id), [workspace.id]);
  const account = await request("/v0/accounts", "POST", { label: "Synthetic Codex", adapter: "codex" });
  const connected = await request(`/v0/accounts/${account.id}/connect`, "POST", {});
  assert.equal(connected.connected, true);
  assert.equal(connected.vault_ref, path.join(home, "accounts", account.id));
  assert((await stat(connected.vault_ref)).isDirectory());
  const renamed = await request(`/v0/accounts/${account.id}`, "PATCH", { label: "Renamed synthetic account" });
  assert.equal(renamed.label, "Renamed synthetic account");
  assert.equal((await request("/v0/accounts")).find(item => item.id === account.id).label, renamed.label);
  const initPath = `/v0/workspaces/${workspace.id}/init`;
  const config = { stack: "Go", methodology: "TDD", test: "never-run-test", build: "never-run-build", install: "never-run-install", execution: "sequential", worktree: "medium+", template: "templates/go.md" };
  const input = { account_id: account.id, config, brief: "Literal notes; $(never-run)", owner: "web", client: "synthetic-journey" };
  assert.equal((await request(initPath, "POST", { brief: "old incomplete request" }, 409)).error, "authenticated_account_required");
  assert.equal((await request(initPath, "POST", { account_id: account.id }, 400)).error, "init");
  assert.equal((await request(initPath, "POST", { ...input, config: { ...config, execution: "invalid" } }, 400)).error, "init");
  assert.equal((await request(initPath, "POST", { ...input, account_id: "absent" }, 404)).error, "init");
  assert.equal((await request("/v0/workspaces/absent/init", "POST", input, 404)).error, "init");
  // Empty PATH and isolated home keep this journey independent of credentials and providers.
  assert.equal((await request(initPath, "POST", input, 409)).error, "cli_missing");
  const listedAccount = (await request("/v0/accounts")).find(item => item.id === account.id);
  assert.equal(listedAccount.readiness.ready, false);
  assert.equal(listedAccount.readiness.code, "cli_missing");
  assert.equal(typeof listedAccount.readiness.checked_at, "string");
  assert.deepEqual(await request("/v0/sessions"), []);
  const readWorkspace = async () => (await request("/v0/workspaces")).find(item => item.id === workspace.id);
  assert.equal((await readWorkspace()).setup.attempt, undefined);
  await mkdir(path.join(workspaceRoot, ".batuta"));
  await writeFile(path.join(workspaceRoot, ".batuta", "profile.md"), "Stack: Go\n");
  assert.equal((await readWorkspace()).setup.ready, false);
  await writeFile(path.join(workspaceRoot, ".batuta", "profile.md"), "# Profile — synthetic\n" + Object.entries(config).map(([key, value]) => `${key[0].toUpperCase()}${key.slice(1)}: ${value}`).join("\n") + "\n");
  await writeFile(path.join(workspaceRoot, ".batuta", "routing.md"), "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | synthetic |\n| medium | * | codex | synthetic |\n| high | * | claude | synthetic |\n| critical | * | self | session |\n");
  assert.equal((await readWorkspace()).setup.configuration_code, "work_missing");
  await writeFile(path.join(workspaceRoot, "WORK.md"), "# Work — synthetic\n\n## In progress\n\n## Done\n");
  const complete = await readWorkspace();
  assert.equal(complete.profile, true);
  assert.deepEqual(complete.setup, { ready: true, state: "ready" });
  const claude = await request("/v0/accounts", "POST", { label: "Synthetic Claude", adapter: "claude" });
  const claudeConnected = await request(`/v0/accounts/${claude.id}/connect`, "POST", {});
  assert.notEqual(claude.id, account.id);
  assert.equal(claudeConnected.adapter, "claude");
  assert.equal(claudeConnected.connected, true);
  assert.equal(claudeConnected.vault_ref, path.join(home, "accounts", claude.id));
  assert.notEqual(claudeConnected.vault_ref, connected.vault_ref);
  const codexVault = await stat(connected.vault_ref);
  const claudeVault = await stat(claudeConnected.vault_ref);
  assert(claudeVault.isDirectory());
  assert.notEqual(`${codexVault.dev}:${codexVault.ino}`, `${claudeVault.dev}:${claudeVault.ino}`);
  const squad = await request("/v0/squads", "POST", {
    name: "Synthetic catalog squad", purpose: "Fixture catalog lifecycle", origin: "custom",
    members: [
      { role: "conductor", cli: "codex", model: "synthetic-codex", account_id: account.id, maestro: true },
      { role: "reviewer", cli: "claude", model: "synthetic-claude", account_id: claude.id, maestro: false },
    ],
  });
  assert.equal(squad.origin, "custom");
  assert.equal(squad.version, 1);
  assert.equal(squad.draft, false);
  assert.deepEqual(squad.members.map(member => member.account_id), [account.id, claude.id]);
  assert.notEqual(squad.members[0].id, squad.members[1].id);
  const first = await request("/v0/missions", "POST", {
    workspace_id: workspace.id, title: "First catalog mission", brief: "First immutable snapshot", squad_id: squad.id,
  });
  const revisedSquad = await request("/v0/squads", "POST", {
    ...squad, name: "Revised catalog squad",
    members: squad.members.map(member => ({ ...member, model: `${member.model}-revised` })),
  });
  assert.equal(revisedSquad.id, squad.id);
  assert.equal(revisedSquad.version, 2);
  const second = await request("/v0/missions", "POST", {
    workspace_id: workspace.id, title: "Second catalog mission", brief: "Second immutable snapshot", squad_id: squad.id,
  });
  assert.notEqual(first.id, second.id);
  assert.equal(first.title, "First catalog mission");
  assert.equal(second.title, "Second catalog mission");
  assert.equal(first.brief, "First immutable snapshot");
  assert.equal(second.brief, "Second immutable snapshot");
  assert.deepEqual(first.squad_snapshot, squad);
  assert.deepEqual(second.squad_snapshot, revisedSquad);
  const readCatalog = async () => {
    const accounts = await request("/v0/accounts");
    assert.deepEqual(accounts.map(item => item.id).sort(), [account.id, claude.id].sort());
    for (const item of accounts) {
      const expected = item.id === account.id ? renamed : claudeConnected;
      assert.equal(item.label, expected.label);
      assert.equal(item.adapter, item.id === account.id ? "codex" : "claude");
      assert.equal(item.vault_ref, expected.vault_ref);
      assert.equal(item.connected, true);
      assert.equal(item.authed, false);
      assert.equal(item.readiness.ready, false);
      assert.equal(item.readiness.code, "cli_missing");
      assert.equal(typeof item.readiness.checked_at, "string");
      assert((await stat(item.vault_ref)).isDirectory());
    }
    const missions = await request(`/v0/missions?workspace=${workspace.id}`);
    assert.deepEqual(missions.map(item => item.id).sort(), [first.id, second.id].sort());
    // Select each identity independently; list order is not mission authority.
    for (const expected of [second, first]) {
      assert.equal(expected.workspace_id, workspace.id);
      assert.equal(expected.squad_id, squad.id);
      assert.equal(expected.status, "open");
      assert.equal(expected.binding, undefined);
      assert.deepEqual(missions.find(item => item.id === expected.id), expected);
    }
    assert.deepEqual(await request("/v0/missions?workspace=absent"), []);
    assert.deepEqual(await request("/v0/sessions"), []);
    const storedSquad = (await request("/v0/squads")).find(item => item.id === squad.id);
    assert.deepEqual(storedSquad, revisedSquad);
    return {
      workspace: await readWorkspace(), squad: storedSquad,
      accounts: accounts.map(({ readiness: { checked_at, ...readiness }, ...item }) => ({ ...item, readiness }))
        .sort((left, right) => left.id.localeCompare(right.id)),
      missions: missions.sort((left, right) => left.id.localeCompare(right.id)),
    };
  };
  const beforeRestart = await readCatalog();
  assert.deepEqual(beforeRestart.workspace, complete);
  await stopDaemon();
  await startDaemon();
  assert.equal(await readFile(path.join(home, "auth.secret"), "utf8"), token);
  assert.equal((await fetch(base + "/v0/workspaces", { signal: AbortSignal.timeout(5000) })).status, 401);
  assert.deepEqual(await readCatalog(), beforeRestart);
  await stopDaemon();
  console.log("PASS: synthetic real-daemon auth, init-negative/configuration checks, two isolated Codex/Claude vaults, custom squad revisions, two distinct mission snapshots and authenticated catalog rereads after owned daemon restart; no CLI/model execution");
} catch (error) {
  failure = error;
  throw error;
} finally {
  try {
    await cleanup();
    console.log("Disposable daemon and data cleaned up");
  } catch (error) {
    if (!failure) throw error;
    console.error("Cleanup failed:", error);
  } finally {
    process.off("SIGINT", interrupted);
    process.off("SIGTERM", interrupted);
  }
}
