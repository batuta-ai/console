<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/readme-header-console-dark.png">
  <img src="assets/readme-header-console-light.png" width="100%" alt="batuta-ai / console — Operação local de agentes: daemon, TUI e web. Local agent operation: daemon, TUI and web.">
</picture>

> 🇧🇷 [Versão em português](README.pt-BR.md)

> *Quem rege não toca.* — The conductor does not play.

# Batuta Console

Local operator surface for agent CLIs. A per-user Go daemon owns sessions
and a versioned HTTP API; a Bubble Tea TUI and a loopback web app are
clients of that daemon. Closing a client does not end a session.

Batuta (`batuta-ai/core`) stays the authority for verified deliveries. The
Console keeps catalog, approval receipts and processes separate from native
delivery evidence. Official sessions require a selected isolated account and
readiness checks. Batuta injection is requested by default; `"batuta": false`
disables injection, not account/readiness guards. Missing required planning,
init or role skills reject the launch; do not assume a generic-session fallback.

Console depends on the published [`batuta-ai/core`](https://github.com/batuta-ai/core)
module; no sibling checkout is required. It is not part of the core module
(core stays daemon-free) and not part of the host plugin package.

## Commands

```
consoled                         start the local daemon (does not open a browser)
console                          start the daemon if needed, then the TUI
console --web                    start the daemon if needed, open the web UI, print the URL
```

Listen address defaults to `127.0.0.1:9477`. State lives under
`$BATUTA_CONSOLE_HOME` or `~/.local/state/batuta/console`. `console --web`
opens the browser with `?token=` from that home. If the port is already
bound by a different home, the client exits with an error instead of
opening a page that cannot authenticate. Pass `-home` to match the
running daemon, or stop it. `consoled` itself never opens a browser.

## Build and install from source

Use Go 1.27.2 or newer and run these commands from a checkout of the desired
Console revision (the qualified SDK here is Go 1.27.2). `go.mod` and `go.sum` pin the dependency graph, including
Batuta core `v1.1.0-beta.49`. The tracked embedded web assets are sufficient
to build both binaries without Node.js.

```bash
export GOWORK=off GOFLAGS=-mod=readonly GOENV=off
export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE=
go mod download
go mod verify
go test ./...
go vet ./...
go build -o bin/consoled ./cmd/consoled
go build -o bin/console ./cmd/console
go install ./cmd/consoled ./cmd/console
```

The builds are written to `bin/`. Keep both executables: `console` finds
`consoled` on PATH, then beside itself. Installation writes both commands to
`GOBIN`, or to `$(go env GOPATH)/bin` when `GOBIN` is unset; add that directory
to `PATH`. The exported variables disable developer workspaces and saved
Go settings for this shell, force module mode instead of vendoring, and enable
public-module checksum verification.

To test, build, and install the current source (including uncommitted changes)
in a disposable directory, run:

```bash
sh scripts/check-standalone.sh
```

This check rejects core module replacements, disables workspaces and developer Go
flags, and ignores the root vendor directory. It verifies downloaded module
cache contents and enables the public checksum database even if the caller
disabled it. Tests and builds use the pinned module graph in read-only mode.
It uses fresh output directories and removes its temporary files on success
or failure. The caller's source tree and shell environment are preserved;
ordinary Go module and build caches may be read and updated. Uncached modules
or checksum records require network access.
These commands verify installation from source. Installation by a public
Console tag (`go install ...@<tag>`) remains pending publication and verification.
The core pre-release is selected for development; stable publication still
requires qualification against a stable core/plugin contract.

## Operator journey (web and TUI)

1. **Account.** Open Accounts; create a labelled Codex or Claude account and
   select its actual catalog ID. Connect prepares that account's private HOME;
   it does not log in. Use **Sign in** (TUI: `l`) to launch the official CLI
   login in that HOME. Complete the login, return to Accounts and refresh.
   Readiness checks the connected isolation, exact supported CLI banner and
   fresh provider authentication with bounded checks. Credential-file presence,
   a connected label or a started/exited login process does not establish readiness.
   In the TUI, connecting an already connected account is a no-op; inspect
   readiness with `f`.
2. **Workspace.** Create/select a workspace by its actual ID. Enter an absolute
   checkout directory or use the native folder picker; if it is unavailable,
   enter the directory manually. Registration alone does not initialize Batuta.
   Review **Stack, Methodology, Test, Build, Install, Execution, Worktree and
   Template**, optional notes, and the explicitly selected ready account.
   Execution is `sequential` or `parallel`; Worktree is `off`, `medium+` or
   `always`. Each required field allows at most 8192 UTF-8 bytes with no NUL or
   line breaks; notes allow 65536 UTF-8 bytes with no NUL. Confirm to start
   official init. Configuration values are literal inputs, not shell commands
   executed by the form. Init asks the CLI to write `.batuta/profile.md`,
   `.batuta/routing.md` and `WORK.md` while preserving existing user content.
3. **Inspect setup.** Select the actual init session to observe it. The latest
   attempt and configuration readiness are separate facts. A stopped/exited
   process does not prove valid configuration. Refresh setup to inspect disk
   configuration and attempt state. The backend can reuse an identical live
   account/input attempt; conflicting live inputs and already complete setup
   reject a new start. An interrupted/unknown session is catalog history, not a
   reconnectable PTY. Inspect before explicitly confirming another setup attempt.
4. **Squad and mission.** Create or edit a squad; select actual member IDs,
   providers, matching accounts, models/effort and named skills, with exactly one
   Maestro. Save stores a version; Validate checks structure/readiness without
   model generation. A stored non-draft squad does not guarantee fresh readiness.
   Validation of a blank draft may use the stored squad and produce a generic
   refusal; inspect the returned identity and keep the draft. Create the mission
   with an explicit workspace, squad and objective. Review the actual returned
   squad snapshot: later squad edits do not rewrite that mission snapshot.
5. **Plan.** Open the mission and choose planning with its snapshot Maestro.
   The CLI receives the mission goal in its launch arguments; the Console does
   not type a planning command into the terminal. Planning starts an interactive
   session, not native execution or approval. Inspect/select the exact returned
   session and attach explicitly; observation does not take input ownership.
   Alternatively, bind an exact existing plan path in the selected workspace.
   The mission does not automatically adopt a nearby plan or journal.
6. **Inspect and approve.** Before native binding, inspect all exact source bytes
   and metadata. Approval records an immutable receipt with a SHA-256 version
   covering every original byte. It neither changes the source's status nor runs
   a model. Before the first start, any source-byte change requires fresh
   inspection/approval. Keep the receipt identity, including its original
   binding with empty delivery/opening fields.
7. **Execute and recover.** Start native execution explicitly from that receipt.
   It requires the selected ready accounts, installed skills, permitted routes
   and a Core CLI matching the linked SDK (`v1.1.0-beta.49`). Start `consoled
   -core /absolute/path/to/batuta` to choose that executable; otherwise it uses
   `batuta` on PATH. This version/capability check does not close the separate
   official Core binary security gate. Inspect the resulting deterministic
   delivery and opening. Resume uses the **original receipt and target plus
   that same delivery/opening**. After native binding, Plan Detail returns the
   **original approved snapshot**, even if the editable source changed or was
   deleted. Current editable bytes are unavailable through that API; there is
   no local-file fallback. Historical receipts remain separate, read-only facts.
8. **Questions and completion.** Answer the current question's exact identity
   (mission, workspace, delivery, task, execution, question ID and opening).
   Recording an answer does not resume execution. Inspect, then explicitly
   resume the original delivery. Done requires the current cleared review gate,
   matching evidence and current projection. Process exit, `active=false`,
   catalog counters, attention flags and output volume are not completion proof.

### TUI controls

Use the keys shown by the focused screen. Accounts: `c`, `n` new, Enter details,
`k` connect, `l` official login, `f` inspect. Workspaces: `w`, `n` new, Enter
select, `e` setup, `f` inspect. Forms use Tab to change fields; review then
ordinary Enter to confirm. Workspace setup uses Ctrl+R for review and Ctrl+A
for account recovery. In text fields, letters and pasted text edit the draft.

In a mission room: `p` planning, `b` bind an existing plan, `o` source detail,
`k` current receipt, `h` historical receipt, `f` authoritative inspection,
`e` start, `g` resume, `j` retained answer, `n` confirm a new answer draft,
`w` full progress/evidence, `l` inspect an exact session ID. In source view,
Up/Down pages through escaped exact bytes; after inspecting all pages, `y`
reviews approval before native binding. Esc exits focused views/forms. In the
mission room, `h` and `w` are local receipt/progress actions, not global
home/workspace navigation. An answered or stale draft stays retained under `j`;
leave the form and use `n` to explicitly confirm replacing it for the current question.

Select a real session with `[` / `]`, `v` attach read-only, `t` take control,
`i` enter child input, `u` release, `x` stop the selected process. Ownership is
separate from attachment. Ctrl+] detaches from child input; child keystrokes
are not navigation shortcuts. See [terminal behavior](docs/tui-terminal.md)
for geometry, Unicode, bounded replay and reconnect limits.

### Recoverable errors

Keep confirmed identities and drafts when an operation fails. A timeout, lost
reply, malformed/foreign response or cancellation after dispatch can leave an
**uncertain effect**. Inspect the actual account/workspace/session, receipt or
native delivery before another mutation; do not automatically repeat it or infer
success from a catalog refresh. Inspection and acknowledgment do not themselves
prove a mutation succeeded. A rejected operation may be corrected and explicitly
submitted again only when its effect and current target are understood.

For `cli_missing`/`cli_incompatible`, inspect the official CLI installation/version;
for disconnected/invalid isolation, inspect the selected account's private home;
for expired/invalid credentials, use its official login and refresh readiness.
Provider/probe failures or cancelled checks require fresh inspection, not another
account substituted silently. Missing selected skills or conflicting discovery
links require reconciling that installation without overwriting existing content.
Stale plan/question, foreign opening, review blocks and owned deliveries require
inspection of their exact current authority. An `unknown` session cannot be
reattached or stopped as though it were owned by this daemon. Reconnecting a
terminal is observation; it is not replay of input, Start, Resume or Answer.

## Beta infrastructure

Console uses its own `v0.1.0-beta.N` sequence, independent of the linked
Core version. The first published version will be `v0.1.0-beta.1`; the manifest
seed `0.1.0-beta.0` is unpublished bookkeeping. No Console release has been
published; repository destination/visibility remain undefined.

With Go 1.27.2, Node 26.10.0, npm 12.2.0 and govulncheck 1.8.0 on PATH:

```sh
sh scripts/check-release.sh       # non-root source checks; failures stop the run
sh scripts/package-release.sh     # also requires GoReleaser 2.18.2; snapshots only
```

Packages in `dist/` contain both commands, embedded web assets, LICENSE and
README, with SHA256 checksums, for darwin/linux on amd64/arm64. Cross-builds
are not native runtime qualification. See [release setup and authority](docs/release.md)
for frozen tools, the reviewed release-PR/tag pipeline, and current proof limits.
No successful GitHub run or Console publication is claimed. Task37's complete
release matrix, stable Core/plugin contract and Core official CLI binary security
qualification remain pending.

## Selected platform and provider matrix

The intended product matrix is **Codex and Claude on macOS, Linux and native
Windows**. The current beta packaging configuration covers macOS/Linux only.
Native operational and full provider, mission, browser and TUI qualification
remain pending. Native Windows build portability (Task42) is complete;
ConPTY/runtime and provider QA (Tasks43–44) remain pending;
the current PTY start/resize implementation rejects Windows operations. WSL
proves Linux only. Other adapter catalog entries remain outside this matrix.
Reliability work remains open: sporadic Linux fork `EPERM`, event-fixture
publication ordering and an unexplained resize-test failure. Full journey QA
still needs stronger PT/error/tick/mutual-exclusion coverage, external plan
rebind reconciliation, abnormal run lifetime and source-page cost checks.
This documentation does not claim those gaps fixed.
Source installation or component cross-compilation does not qualify a release.

`GET /v0/capabilities` retains `api`, `product`, `lang`, `integrations`, the
`adapters` string list and the existing `features` keys. New typed objects expose:

- `runtime`: the running binary's `os`, `arch`, `go_version`, and `selected`
  platform status. `build.version` comes from Go build information, including a
  VCS-derived pseudo-version for an untagged Git build, or `(devel)` when
  reported by the toolchain. Unavailable version/revision values are `unknown`,
  and unavailable VCS modification status is `null`. This is build provenance,
  not qualification or a claim that the current source matches that binary.
- `matrix`: selected `platforms` (`darwin`, `linux`) and `providers` (`codex`,
  `claude`), with release `qualification.status = pending`.
- `pty`: `supported` means the running platform has the selected native backend.
  `available` means a request-local PTY allocation, resize and descriptor close
  succeeded, without launching a process. `code` is `pty_available`,
  `pty_unavailable`, `check_cancelled` or `platform_unsupported`. The legacy
  `features.pty` mirrors `available`; it is false on Windows and on observation
  failure. Availability is a point-in-time observation, not a launch guarantee.
  Native backend availability still leaves operational qualification `pending`;
  excluded platforms have qualification `unsupported`.
- `providers`: each catalog adapter has `id`, `cli`, `selected`, `supported`,
  its existing static `isolation` label, and separate evidence facts.
  `installation.status` is `present`/`missing` (`cli_present`/`cli_missing`) from
  executable PATH lookup, or `not_checked`/`check_cancelled`. Presence includes
  outside-matrix executables and never proves compatibility or readiness.
  `compatibility` is `not_checked`/`isolated_account_required` on the selected
  platform/provider pairs, otherwise `unsupported` with `adapter_unsupported`
  or `platform_unsupported`. `compatible_banners` shares the existing per-account
  readiness policy:
  Codex `codex-cli 0.160.0`; Claude `2.1.286 (Claude Code)` and
  `2.1.289 (Claude Code)`. Banners describe the policy, not an observed version.
  `account_readiness` is always `not_checked`/`account_readiness_required`;
  `qualification` is `pending`/`native_operation_qualification_pending` for
  selected pairs, otherwise `unsupported` with the same exclusion diagnostic.

Capabilities queries inspect no account credentials, authenticate with no
provider, execute no CLI and mutate no catalog or account state. Local filesystem
and PTY syscalls are synchronous; cancellation is checked around observations,
with PTY resources closed before returning. Cancellation cannot interrupt an
in-progress syscall, and no hard deadline is guaranteed for synchronous syscalls.
Diagnostics omit private paths and raw error/output text. A cancelled query does not imply installation absence.
The existing per-account `AccountReadiness` checks remain the authority for
login, init and execution: an explicit connected isolated account, bounded exact
CLI version check and fresh provider authentication are separate requirements.
Static isolation labels, installed executables, synthetic version fixtures and
native PTY availability cannot substitute for these guards or full release QA.
These observations do not enable blocked operations or replace per-account checks.

## Develop

The web UI lives in `web/` and is embedded from `internal/webui/dist`.
It consumes [`@lyra-ds/react`](https://www.npmjs.com/package/@lyra-ds/react) and
[`@lyra-ds/styles`](https://www.npmjs.com/package/@lyra-ds/styles); Batuta tokens
overlay those packages in `web/src/batuta-theme.css`.

```bash
npm --prefix web ci
npm --prefix web run build
go build -o bin/consoled ./cmd/consoled
```

The Go embed directive requires `internal/webui/dist` at compile time.
Tracked assets allow a source build without Node; after web source edits,
regenerate them with the pinned Node 26.10.0/npm 12.2.0 build above. Missing
embedded assets prevent compilation: an API-only diagnostic handler is not
a fallback for a missing embed directory.

For optional development against a local core checkout, create a separate
workspace in a parent directory containing `console/` and `core/`:

```bash
go work init ./console ./core
cd console
GOWORK="$(pwd)/../go.work" go test ./...
```

Keep this developer workspace local. Do not add a `replace` directive to
Console's `go.mod`; the standalone check always uses the published dependency.

Account connect creates an isolated HOME for that catalog row. Official
CLI login happens later in the session PTY using that HOME — the API
never accepts a secret.

## Documentation

- [Domain](docs/domain.md)
- [HTTP and WebSocket API](docs/api.md)

Repository documentation is English, with a Brazilian Portuguese sibling
README. Product UI strings (TUI and web) ship
in English with a Portuguese pack selected by `BATUTA_LANG`, then `LC_ALL`
or `LANG`. The web client reads `lang` from `GET /v0/capabilities` so it
matches the daemon, not the browser locale.

Language changes translate product labels and official/custom skill summaries.
Stored squad names, roles and purposes (including seeded templates), user text,
provider output, source bytes, IDs and receipt/evidence provenance remain data.

## Repository navigation and qualification notes

- [Documentation index](docs/README.md)
- [Local presentation assets and regeneration](docs/branding.md)

The Windows build work (Task42) is complete; native Windows runtime (Task43)
is unqualified and excluded from this source branch. Real-provider operation
(Task44) and final release qualification remain pending. Build evidence does
not establish operational support. The original operator documentation above
retains its qualified source boundaries; no Console package or tag is published.
