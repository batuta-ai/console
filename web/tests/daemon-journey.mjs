import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, readFile, rm, stat } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = fileURLToPath(new URL("../../", import.meta.url));
const temporary = await mkdtemp(path.join(os.tmpdir(), "console-journey-"));
let daemon;
let exited;
let probe;
const cleanup = async () => {
  if (probe?.listening) await new Promise((resolve, reject) => probe.close(error => error ? reject(error) : resolve()));
  if (daemon && daemon.exitCode === null && daemon.signalCode === null) {
    daemon.kill("SIGTERM");
    const deadline = setTimeout(() => daemon.kill("SIGKILL"), 5000);
    try { await exited; } finally { clearTimeout(deadline); }
  }
  await rm(temporary, { recursive: true, force: true });
};
const interrupted = () => { void cleanup().then(() => process.exit(130)); };
process.once("SIGINT", interrupted);
process.once("SIGTERM", interrupted);
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
  const build = spawnSync("go", ["build", "-o", binary, "./cmd/consoled"], { cwd: repository, encoding: "utf8" });
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
  daemon = spawn(binary, ["-home", home, "-listen", `127.0.0.1:${port}`], { env: environment, stdio: ["ignore", "ignore", "pipe"] });
  exited = once(daemon, "exit");
  let log = "";
  await new Promise((resolve, reject) => {
    const deadline = setTimeout(() => reject(new Error(`daemon startup timeout: ${log}`)), 10000);
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
  const token = await readFile(path.join(home, "auth.secret"), "utf8");
  const base = `http://127.0.0.1:${port}`;
  const request = async (endpoint, method = "GET", body) => {
    const response = await fetch(base + endpoint, {
      method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(5000),
    });
    assert.equal(response.status, 200, `${endpoint}: ${await response.clone().text()}`);
    return response.json();
  };
  assert.equal((await fetch(base + "/v0/workspaces", { signal: AbortSignal.timeout(5000) })).status, 401);
  assert.deepEqual(await request("/v0/workspaces"), []);
  const workspace = await request("/v0/workspaces", "POST", { name: "Synthetic workspace", root: workspaceRoot });
  assert.equal(workspace.name, "Synthetic workspace");
  assert.equal(workspace.root, workspaceRoot);
  assert.deepEqual((await request("/v0/workspaces")).map(item => item.id), [workspace.id]);
  const account = await request("/v0/accounts", "POST", { label: "Synthetic Codex", adapter: "codex" });
  const connected = await request(`/v0/accounts/${account.id}/connect`, "POST", {});
  assert.equal(connected.connected, true);
  assert.equal(connected.vault_ref, path.join(home, "accounts", account.id));
  assert((await stat(connected.vault_ref)).isDirectory());
  const renamed = await request(`/v0/accounts/${account.id}`, "PATCH", { label: "Renamed synthetic account" });
  assert.equal(renamed.label, "Renamed synthetic account");
  assert.equal((await request("/v0/accounts")).find(item => item.id === account.id).label, renamed.label);
  assert.deepEqual(await request("/v0/sessions"), []);
  console.log("PASS: synthetic real-daemon auth, workspace persistence, isolated account connect/rename; no CLI/model execution");
} finally {
  process.off("SIGINT", interrupted);
  process.off("SIGTERM", interrupted);
  await cleanup();
  console.log("Disposable daemon and data cleaned up");
}
