#!/bin/sh
set -eu
[ "$#" = 1 ] && [ "$1" = --fixtures ] || { echo 'Usage: qualify-release.sh --fixtures' >&2; exit 2; }
case "$(uname -s)" in
    Darwin|Linux) ;;
    *) echo 'Fixture qualification requires native macOS or Linux; Windows and WSL do not qualify native Windows.' >&2; exit 2 ;;
esac
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
exec node --input-type=module <<'JAVASCRIPT'
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const temporary = await mkdtemp(path.join(os.tmpdir(), "console-qualification-"));
let interrupted = false;
const environment = {
  PATH: process.env.PATH, HOME: path.join(temporary, "home"), TMPDIR: temporary,
  XDG_CONFIG_HOME: path.join(temporary, "config"), XDG_DATA_HOME: path.join(temporary, "data"),
  XDG_STATE_HOME: path.join(temporary, "state"), GOCACHE: path.join(temporary, "go-cache"),
  GOTOOLCHAIN: "local", GOENV: "off", GOWORK: "off", GOFLAGS: "-mod=readonly",
  GOPROXY: "off", GOSUMDB: "sum.golang.org", GOPRIVATE: "", GONOPROXY: "", GONOSUMDB: "",
  GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1",
};
for (const key of ["GOROOT", "GOPATH", "GOMODCACHE"]) {
  if (process.env[key]) environment[key] = process.env[key];
}
const stop = () => { interrupted = true; };
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
const run = async (label, command, args, timeout = 180000, env = environment, cwd = process.cwd()) => {
  assert.equal(interrupted, false, "qualification interrupted");
  console.log(`STAGE ${label}\nCOMMAND ${JSON.stringify([command, ...args])}`);
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { env, cwd, detached: true, stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    let failure;
    let escalation;
    // detached creates this stage's owned process group; no PID files or foreign discovery.
    const signalGroup = signal => {
      if (!child.pid) return;
      try { process.kill(-child.pid, signal); } catch (error) {
        if (error.code !== "ESRCH") failure = error;
      }
    };
    const terminate = () => {
      signalGroup("SIGTERM");
      escalation ??= setTimeout(() => signalGroup("SIGKILL"), 10000);
    };
    const signalHandler = () => terminate();
    process.on("SIGINT", signalHandler);
    process.on("SIGTERM", signalHandler);
    const deadline = setTimeout(() => {
      failure = new Error(`${label}: exceeded ${timeout}ms`);
      terminate();
    }, timeout);
    child.stdout.on("data", chunk => { output += chunk; process.stdout.write(chunk); });
    child.stderr.on("data", chunk => process.stderr.write(chunk));
    child.once("error", error => { failure = error; });
    child.once("close", (code, signal) => {
      clearTimeout(deadline);
      clearTimeout(escalation);
      process.off("SIGINT", signalHandler);
      process.off("SIGTERM", signalHandler);
      if (failure || interrupted || code !== 0) {
        reject(failure ?? new Error(`${label}: exit=${code} signal=${signal} interrupted=${interrupted}`));
      } else {
        console.log(`PASS ${label}`);
        resolve(output);
      }
    });
  });
};
const fixtures = [
  ["./internal/tui", [
    "TestOuterConsolePTYCatalogApprovalAndRestart",
    "TestJourneyKeyboardPrioritizesBackgroundRefresh",
    "TestJourneyBackgroundRefreshPreservesMutationFailure",
    "TestJourneyBackgroundRefreshPreservesUncertainFailure",
    "TestJourneyPublicCoreQuestionResumeAndRestart",
    "TestJourneyRealDaemonPlanningBindingApprovalTwoMissions",
    "TestAccountOfficialLoginOpensActualTerminal",
    "TestWorkspaceInitActualHTTPWebSocketPTY",
    "TestTerminalNativeRawInput",
    "TestTerminalNativeCanonicalSignalsEOF",
    "TestTerminalNativeOwnershipResizeDetach",
    "TestTerminalNativeExplicitStop",
    "TestTerminalNativeActorMaximumHeap",
    "TestTerminalNativeFragmentedPromptFinalGlyph",
    "TestTerminalNativeUnsupportedGeometryAndRecovery",
  ]],
  ["./internal/api", [
    "TestNativeApprovedHTTPJourneyUsesOneDeliveryAfterAnswerAndDeletion",
    "TestTerminalNativeTypedReplayLiveResize",
    "TestTerminalNativeLegacyResizeAndForeignOwner",
    "TestTerminalNativeAuthenticationAndNegotiation",
    "TestTerminalNativeOverflowAndWriteBackpressure",
    "TestTerminalNativeRapidAttachDetachAndExit",
    "TestTerminalNativePTYOrderingBarrier",
    "TestTerminalNativeTinyOutputBarriers",
    "TestTerminalNativeFragmentedOversizedBinary",
  ]],
];
try {
  await mkdir(environment.HOME);
  assert(["darwin", "linux"].includes(process.platform), "native macOS/Linux required");
  assert.equal(process.version, "v26.10.0", "Node pin");
  console.log(`Node ${process.version}; platform ${process.platform}/${process.arch}`);
  assert.equal((await run("npm toolchain", "npm", ["--version"])).trim(), "12.2.0");
  // Locate only the module cache; personal Go configuration is disabled.
  if (!environment.GOMODCACHE) {
    const cacheEnv = { ...environment, HOME: process.env.HOME };
    environment.GOMODCACHE = (await run("cached modules", "go", ["env", "GOMODCACHE"], 10000, cacheEnv)).trim();
  }
  const goVersion = (await run("Go toolchain", "go", ["version"])).trim();
  assert.match(goVersion, /^go version go1\.27\.2 /);
  await run("cached module integrity", "go", ["mod", "verify"]);
  const coreDirectory = (await run("Core module directory", "go", ["list", "-m", "-f", "{{.Dir}}", "github.com/batuta-ai/core"], 10000)).trim();
  assert(path.isAbsolute(coreDirectory), "resolved Core module directory required");
  await run("Core cached module integrity", "go", ["mod", "verify"], 180000, environment, coreDirectory);
  for (const [pkg, names] of fixtures) {
    console.log(`REQUIRED ${pkg}\n${names.join("\n")}`);
    const listed = new Set((await run(`list ${pkg}`, "go", ["test", pkg, "-list", "^Test", "-timeout=120s"])).split(/\r?\n/));
    for (const name of names) assert(listed.has(name), `Missing required fixture: ${pkg}/${name}`);
  }
  const provenance = [];
  const revision = (await run("current source revision", "git", ["rev-parse", "HEAD"], 10000)).trim();
  assert.match(revision, /^[a-f0-9]{40}$/);
  for (const binary of ["console", "consoled"]) {
    const output = path.join(temporary, binary);
    await run(`build ${binary}`, "go", ["build", "-buildvcs=true", "-o", output, `./cmd/${binary}`]);
    const embedded = JSON.parse(await run(`Go build information ${binary}`, "go", ["version", "-m", "-json", output], 10000));
    assert.equal(embedded.GoVersion, "go1.27.2");
    assert.equal(embedded.Path, `github.com/batuta-ai/console/cmd/${binary}`);
    assert.equal(embedded.Main.Path, "github.com/batuta-ai/console");
    assert.equal(embedded.Main.Replace, undefined);
    const settings = Object.fromEntries(embedded.Settings.map(({ Key, Value }) => [Key, Value]));
    assert.equal(settings.vcs, "git");
    assert.equal(settings["vcs.revision"], revision);
    assert(["true", "false"].includes(settings["vcs.modified"]), "embedded VCS modified state required");
    const core = embedded.Deps.find(module => module.Path === "github.com/batuta-ai/core");
    assert.equal(core?.Version, "v1.1.0-beta.55.1");
    assert.equal(core.Replace, undefined);
    const info = JSON.parse(await run(`provenance ${binary}`, output, ["-version"], 10000));
    assert.equal(info.binary, binary);
    assert.equal(info.go_version, embedded.GoVersion);
    assert.equal(info.go_authority, "runtime.Version");
    assert.deepEqual(info.release, { version: "development", revision: "unknown", authority: "unset" });
    assert.deepEqual(info.observed, {
      version: embedded.Main.Version, revision, modified: settings["vcs.modified"] === "true", authority: "go_build_info",
    });
    assert.deepEqual(info.core, { status: "linked", version: core.Version, replacement_version: "none", authority: "go_build_info" });
    assert.equal(info.compatibility.stable.status, "pending");
    provenance.push(info.observed);
  }
  assert.deepEqual(provenance[0], provenance[1]);
  await run("real daemon HTTP catalog and restart", process.execPath, ["web/tests/daemon-journey.mjs"], 240000);
  for (const [pkg, names] of fixtures) {
    // The heap fixture alone retains its unchanged ten-minute context.
    const raw = await run(`fixtures ${pkg}`, "go", ["test", pkg, "-count=1", "-timeout=20m", "-json", "-run", `^(${names.join("|")})$`], 22 * 60 * 1000);
    const events = raw.trim().split(/\r?\n/).map(line => JSON.parse(line));
    assert(!events.some(event => event.Action === "skip"), `${pkg}: skipped fixture`);
    for (const name of names) {
      assert(events.some(event => event.Test === name && event.Action === "pass"), `${pkg}: required fixture did not pass: ${name}`);
    }
  }
  console.log(`PASS: fixture-only qualification (revision=${revision}, modified=${provenance[0].modified}). Browser, outer Console PTY, clean-install bundle discovery, native Windows, real providers, stable release and whole Task38 remain unqualified.`);
} finally {
  await rm(temporary, { recursive: true, force: true });
  process.off("SIGINT", stop);
  process.off("SIGTERM", stop);
  console.log("Disposable qualification output cleaned up");
}
JAVASCRIPT
