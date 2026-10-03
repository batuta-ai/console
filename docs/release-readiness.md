# Release readiness — Batuta Console

Date: 2026-10-02. Reviewed Console commit: `0461003`. Local core commit: `b33cc9a`.

**Verdict: REWORK — not ready for a stable release.** The daemon, catalog, account directories, basic PTY transport, TUI and web build exist. The remaining work includes execution correctness and a complete mission lifecycle, as well as packaging. This is not a recommendation to rebuild the product: keep the daemon/client split and the core journal as the authority for verified work.

The review covers manifests, command entry points, adapters, accounts, service, session manager, SQLite store, API/WebSockets, Batuta integration, limits, TUI, React flows, localization, tests and release materials. Acceptance criteria were derived from the requested stable publication; no prior product specification or implementation plan was associated with the task. No production source was changed. Evidence and limitations are in [release-readiness-evidence.md](release-readiness-evidence.md).

Priorities: **P0** blocks the proposed stable product; **P1** must be fixed for a feature included in that release; **P2** improves reliability or can be deferred if its limitation is explicit. Some P1 findings can be resolved by narrowing the supported product. There are 28 findings: 6 P0, 14 P1 and 8 P2.

## What was verified

| Check | Result | Meaning |
| --- | --- | --- |
| `go test ./...` | Pass | Existing tests pass with the sibling core present. |
| `go vet ./...` | Pass | No vet diagnostics. |
| Build `console` and `consoled`, macOS arm64 | Pass | Both local binaries compile. |
| `npm ci`, `npm run build`, `npx tsc --noEmit` | Pass | Web dependencies, production build and strict type checking work. |
| Regenerated embedded web assets | Identical to tracked assets | No observed source/dist drift at this commit. |
| `go test -race ./...` | Fail | A test data race and a SQLite lock failure. |
| `go test -race ./internal/api -run TestNaturalExitIsExited -count=5` | Fail, 5/5 | Session-list/natural-exit concurrency reliably hits `SQLITE_BUSY` in this run. |
| Go statement coverage | 44.9% | Important new-user and lifecycle paths remain untested. |
| Released core `v1.1.0-beta.21`, using a temporary modfile without the local replace | Pass | The declared published dependency works with existing tests; this does not establish compatibility with a future stable tag. |
| Linux amd64 daemon cross-build | Pass | Compilation verified; Linux runtime and desktop interactions were not tested. |
| Windows amd64 daemon cross-build | Fail | `syscall.Flock` and lock constants are unavailable. |
| `npm audit --json` | 0 known vulnerabilities reported | Snapshot of the resolved web dependency graph, not a security guarantee. |
| `govulncheck ./...` | Fail | Six reachable-symbol reports in Go 1.26.4's standard library. |
| Live API/PTY experiments | Multiple defects reproduced | Temporary catalog, fake CLI programs and synthetic credentials; no model calls or user account login. |
| Independent Claude `sonnet` review | Completed, read-only guard passed | Findings were individually judged; corrections are recorded in the evidence document. |

## P0 — stable release blockers

### R01. Standalone installation depends on a sibling checkout

Location: [go.mod](../go.mod), line 45.

`replace github.com/batuta-ai/core => ../core` overrides the published dependency. A clean checkout without that sibling cannot build normally, and versioned `go install` does not support publishing a main module with this local replacement. Tests currently exercise the sibling rather than the version written in `require`.

Use a published, selected core tag in the release module and a developer-only workspace for local integration. A temporary modfile without the replacement passed against beta.21, so removing the replacement does not by itself require a redesign. Acceptance: build and install both binaries from an isolated source checkout and the release tag, with no sibling repository.

### R02. The pinned Go toolchain has reported vulnerabilities

Location: [go.mod](../go.mod), line 3.

The active Go 1.26.4 toolchain produced six reachable-symbol findings in `govulncheck`. Their IDs and fixed versions are recorded in the evidence document. The reports include `net/url`, `net/http`, TLS and ASN.1; the scanner's call paths do not establish that every issue is exploitable through this loopback HTTP configuration. Go 1.26.6 fixes these six reported issues on the 1.26 branch. [Go's net/url report](https://pkg.go.dev/vuln/GO-2026-6218), [HTTP report](https://pkg.go.dev/vuln/GO-2026-6089) and [TLS report](https://pkg.go.dev/vuln/GO-2026-6090) document affected versions.

Select a patched build toolchain, record it in CI and release metadata, and rescan the final module and binaries. Acceptance: no reachable vulnerability findings in the supported release build; remaining package/module-only reports individually assessed.

### R03. Planning is launched, but approval and execution have no complete product flow

Locations: [internal/service/service.go](../internal/service/service.go), line 591; [web/src/App.tsx](../web/src/App.tsx), line 941; [internal/api/server.go](../internal/api/server.go), line 38.

Every injected session with a brief receives a planning instruction that explicitly says not to implement and to wait for approval. The room lists plan names/statuses and a stepper, but cannot display the full plan, approve a specific revision, or start/resume its core loop. Starting another session sends the planning instruction again. A user may manually drive the CLI terminal, but that is not the complete workflow suggested by the room.

Add an explicit mission lifecycle: objective → proposed plan → human approval of that plan → core execution → waiting/answer/resume → verified result. Core must continue owning execution, gates and journals. Acceptance: finish one small mission from the supported UI without manually editing status files or using an unrelated external terminal; record which plan revision the user approved.

### R04. Batuta injection reports success for a root the core rejects

Locations: [integrations/batuta/inject.go](../integrations/batuta/inject.go), lines 48–69; [internal/adapters/adapters.go](../internal/adapters/adapters.go), line 97.

The console discovers `~/.agents/skills` rather than the Batuta directory containing `adapters/`, and does not search the current Codex/Claude plugin caches. In this environment, `Prepare` returned `Enabled=true`, while core `loop.FindSkills` rejected that exact selected root. The child CLI's account HOME also hides the normal host skill locations. Exporting `BATUTA_SKILLS` is useful to core but does not prove that each CLI loaded the Batuta host skills.

Share the core's installation discovery contract and distinguish the core skill directory from host skill registration. Provision required host skills/configuration for each isolated account without sharing login credentials. Acceptance: a fresh isolated account finds the actual skill, completes init and planning, and invokes core using the selected installation; an invalid installation is shown as unavailable rather than injected.

### R05. Missions are not linked to deliveries

Locations: [internal/domain/domain.go](../internal/domain/domain.go), line 83; [integrations/batuta/watch.go](../integrations/batuta/watch.go), line 38; [web/src/App.tsx](../web/src/App.tsx), lines 138, 633 and 793.

The mission stores a squad snapshot but no delivery reference. Watch defaults to the newest journal in the workspace. `activeMissionMatch` checks only that a mission has a title and the watch has a delivery. The mission list uses one watch percentage for every row, and a room can show another mission's gates, tasks or question. Switching workspace also leaves `activeMission` in state, so refresh can keep watching the old workspace.

Persist and select the exact delivery/plan associated with a mission. Use per-mission status/progress and clear or deliberately retain navigation context. Acceptance: two missions in one workspace, and missions across two workspaces, never share progress, attention, plans or evidence by accident.

### R06. Human answers do not identify the displayed question

Locations: [web/src/App.tsx](../web/src/App.tsx), line 1078; [integrations/batuta/watch.go](../integrations/batuta/watch.go), lines 104–108 and 136–139.

The web answer sends workspace, task and text, omitting delivery and question ID. The integration consequently falls back to `loop.Answer`, which searches deliveries for a waiting task. Common task IDs can collide, and the question may change after the page renders. The DTO carries question text but no stable question ID.

Expose the question identity and answer through the delivery-specific core API. Reject stale questions instead of silently applying an answer to a newer one. Acceptance: concurrent deliveries with the same task ID receive only their own answer, and replacing an outstanding question makes an old submission fail clearly.

## P1 — correctness and supported feature gaps

### R07. Account sessions inherit daemon credentials

Location: [internal/session/manager.go](../internal/session/manager.go), line 57.

The account-specific HOME is overlaid on all of `os.Environ()`. A synthetic `OPENAI_API_KEY` set on the temporary daemon was visible inside a supposedly isolated account session. Changing HOME alone therefore does not isolate identity; inherited provider tokens/config paths may select a different identity from the account shown in the UI.

Define a per-adapter environment policy that separates execution environment from authentication material. Acceptance: two accounts cannot inherit each other's or the daemon's provider credentials, while explicitly approved project/build variables still work. Qualify this with synthetic credentials and official CLI login checks.

### R08. Session capacity checks are not atomic

Location: [internal/service/service.go](../internal/service/service.go), lines 379–436.

The running-count check and process/catalog creation are separate operations. Ten simultaneous starts against a cap of three accepted nine sessions in the experiment. The cap is a local concurrency policy, not a provider quota, but the product must enforce the policy it shows.

Reserve capacity atomically per account, release reservations on every failure/exit, and apply the same lifecycle policy to login/init where appropriate. Acceptance: concurrent requests never exceed the cap and failed starts do not consume capacity.

### R09. Stop does not stop the process tree

Locations: [internal/session/manager.go](../internal/session/manager.go), line 163; [cmd/consoled/main.go](../cmd/consoled/main.go), line 64.

Stop kills only the leader PID. A child `/bin/sleep` that inherited an ignored SIGHUP survived the leader's death in the test. Agent CLIs can launch core loops, MCP services and test runners. Daemon shutdown also lacks a deliberate session cleanup policy.

Own and terminate the session process group with a bounded graceful stop and escalation, and define daemon-shutdown behavior. A disconnected client must still leave the session running. Acceptance: stopping a session ends its test children; closing a client does not; daemon shutdown behaves according to the documented policy. Never signal a recovered PID without validating process identity.

### R10. Natural exit and catalog reads cause SQLite lock failures

Locations: [internal/store/store.go](../internal/store/store.go), lines 18 and 317–334; [internal/service/service.go](../internal/service/service.go), line 31.

`ListSessions` holds an outer read cursor while issuing a second query for each row. Exit callbacks write session/event rows concurrently. The race suite hit `SQLITE_BUSY`; the focused natural-exit API test failed all five repetitions. Raising the busy timeout alone would not establish correct concurrency.

Read sessions in one query, define the SQLite read/write strategy, and make callback errors observable. Acceptance: repeated natural exits, session listing and concurrent catalog writes pass under the race detector without 500s or lost exit state. Avoid imposing a single connection while retaining nested queries, which can introduce a different deadlock.

### R11. Capacity recovery depends on a client GET; fast exits can overwrite state

Locations: [internal/service/service.go](../internal/service/service.go), lines 429–436, 443–451, 532 and 581; [internal/api/server.go](../internal/api/server.go), line 340.

After a restart with three catalog rows marked running, a new start returned capacity-full. After `GET /v0/sessions` reconciled the rows, the same start succeeded. Recovery must precede accepting new starts. Separately, Start schedules an exit callback before service persists its initial running snapshot; a sufficiently fast exit can write exited first and then be overwritten by running. That write-order defect is evident in code; a distinct live reproduction was not completed.

Make startup reconciliation and state transitions deterministic; prevent a late initial write from replacing a terminal state. Acceptance: immediate post-restart starts do not require a prior list request, and fast success/failure exits remain exited. Catalog-only recovery rather than PTY resurrection is an acceptable documented contract.

### R12. Server validation does not enforce mission/member/account bindings

Locations: [internal/service/service.go](../internal/service/service.go), lines 136–166, 170–184 and 330–417.

The API accepted a mission with a nonexistent workspace, a session using a nonexistent member, a disconnected account, and a Codex session with a Claude account. Validation does not reject unknown CLI names consistently or ensure account/adapter compatibility and usable login. A nonexistent member can fall through to the default CLI without its intended account. Sessions started with the default member also store the input's empty member ID rather than the resolved member ID. The web readiness check is stricter than the backend and can be bypassed by other clients.

Centralize validation in the service. Generic sessions can remain a supported mode, but their rules must be explicit. Acceptance: invalid references, incompatible accounts and unavailable required identities fail before process creation; valid generic sessions and resolved member attribution are tested.

### R13. Model and reasoning settings do not reach the CLI

Location: [internal/service/service.go](../internal/service/service.go), lines 364–397.

The binding resolves model/effort, and the editor saves them, but the generated argv contains only the executable and prompt. A fake Codex session configured with `audit-model` and `high` received neither setting. The user can see one model while execution uses the CLI's default.

Use per-adapter command construction for supported model and reasoning flags, and show the effective configuration. Acceptance: fake CLI assertions verify exact model/effort/prompt arguments, followed by a real CLI capability check for every supported adapter.

### R14. Missing CLIs become a shell; three adapters lose the objective

Locations: [internal/service/service.go](../internal/service/service.go), lines 394–400 and 600–610.

A missing Cursor executable returned HTTP 200, adapter `cursor-agent`, and argv `/bin/sh`. The intended agent never started. `cliPrompt` also returns no prompt for Cursor, OpenCode and Gemini, so their mission sessions omit the objective and planning instruction. Keeping the objective in an environment variable does not establish that those CLIs read it.

Fail clearly when a required executable is absent. Implement each supported prompt contract or remove that adapter from mission execution until qualified. Acceptance: no agent start silently becomes a shell; each supported adapter receives the complete objective; a separately requested generic shell is clearly labeled.

### R15. Authentication readiness cannot work for all advertised adapters

Locations: [internal/adapters/adapters.go](../internal/adapters/adapters.go), lines 57–77; [web/src/App.tsx](../web/src/App.tsx), line 466.

OpenCode, Gemini and Cursor always return false from `LoggedIn`, while web squad readiness requires every member's account to be authed. These adapters cannot make a squad usable in the web mission flow, even after a successful CLI login. Codex/Claude detection accepts any nonempty file; it does not validate expiry or actual CLI login status. Catalog connect and official login are correctly separate concepts, but the execution readiness contract is incomplete.

Define connected/authenticated/available/unsupported states per adapter, using read-only official probes where possible. Acceptance: a supported adapter can complete the new-user path and invalid/expired synthetic credential files do not certify a usable account. Unsupported adapters remain visible only with explicit limits.

### R16. Web progress can freeze while core keeps working

Locations: [web/src/App.tsx](../web/src/App.tsx), lines 151–181; [internal/api/server.go](../internal/api/server.go), line 490.

The event socket reflects SQLite catalog events, not journal or plan-file changes. Periodic web refresh runs only on login/setup screens. A core loop can append gates or park for input without a new catalog event, leaving the mission screen unchanged. The event socket has no close/error reconnection, and overlapping refreshes have no cancellation or generation guard.

Provide live journal/plan updates through a watch stream or bounded polling, socket reconnect with resynchronization, and stale-response protection. Acceptance: an externally running core delivery updates the room and attention state while its agent process remains alive, and restarting the daemon restores updates without reloading the page.

### R17. Workspace setup has no robust completion/resume contract

Locations: [internal/service/service.go](../internal/service/service.go), lines 42–53, 57–73, 539–645; [web/src/App.tsx](../web/src/App.tsx), line 1612.

Workspace creation accepts nonexistent directories and file paths. Setup readiness checks only `profile.md`, while Batuta init also requires routing and WORK state. A partially written profile blocks StartInit. The web form gathers only stack/test/methodology, and its only entry into setup is creation of a workspace; leaving setup and opening an existing incomplete workspace has no equivalent restart action. `initCLI` can choose an ambient host account when no supported isolated account is usable.

Validate workspace directories, model initialization as a recoverable operation, collect/confirm required configuration, and expose setup for existing workspaces. Account selection must be explicit. Acceptance: cancellation, failed init and partial files can be resumed, and ready means the full core/skill contract is satisfied.

### R18. Windows has no functional release path

Locations: [internal/runtime/runtime.go](../internal/runtime/runtime.go), line 106; [internal/session/pty_windows.go](../internal/session/pty_windows.go), line 11; [internal/api/server.go](../internal/api/server.go), line 112.

The Windows build fails at Unix-only flock usage. Even after that is fixed, PTY startup explicitly returns an unsupported error, while capabilities report `pty: true`. Existing platform-specific browser files do not constitute Windows support.

For the first stable release, exclude Windows and advertise accurate runtime capabilities, or implement/qualify native locking and ConPTY. Acceptance: every shipped platform can log in, initialize, start, resize and stop sessions; unsupported capabilities are reported honestly. Linux compilation also needs runtime qualification before shipping Linux as supported.

### R19. Release automation and version diagnostics are absent

Locations: repository root; [cmd/console/main.go](../cmd/console/main.go), line 15; [cmd/consoled/main.go](../cmd/consoled/main.go), line 23; [README.md](../README.md), line 33.

There is no CI/release workflow or release configuration in this checkout, no version command/embedded build metadata, and no configured Git remote. Installing the console alone also requires finding the separate daemon; an archive/installer should supply both. Embedded web assets are tracked and currently reproducible, but that does not prove every release regenerates them.

Define the target repository, tags, core compatibility and a single repeatable build that runs `npm ci`, typecheck/build, Go checks and packaging. Produce both binaries, checksums, licenses and release notes with version/API/core diagnostics. Acceptance: a release candidate built in clean CI installs on a fresh supported machine and `console --web` starts its bundled daemon.

### R20. The default TUI cannot onboard a fresh installation

Locations: [internal/tui/tui.go](../internal/tui/tui.go), lines 109–241 and 423–485; [internal/tui/pty.go](../internal/tui/pty.go), line 24; [cmd/console/main.go](../cmd/console/main.go), line 43.

The default `console` opens the TUI, which has no complete workspace/account/login/mission creation flow. Its squad editor cannot populate all the fields required for a usable new squad. Terminal output is accumulated as text inside the Bubble Tea view rather than maintained in a terminal emulator, so interactive CLI screen behavior still requires qualification. This terminal issue was reviewed in source, not through an interactive TUI test.

Choose a supported first-release role: implement full TUI onboarding/terminal behavior, or clearly make it an existing-session monitor and guide fresh users to the web. Acceptance: the advertised default entry point reaches a functional workflow, and the TUI's promised controls work with a real agent CLI.

## P2 — reliability, maintenance and documented limitations

### R21. Old limit files are labeled current

Location: [plugins/limits/disk.go](../plugins/limits/disk.go), lines 45–66.

A seven-day-old fixture was reported as `current` with `observed_at` set to the read time. There is no age, range or provider-schema validation, and this repository does not establish that the official CLIs produce the expected `rate_limit.json` schema. The fallback to unknown is otherwise appropriate.

Preserve measurement time/source, validate percentages and expire observations. Qualify an actual collector for every supported source or document import-only/unknown behavior. Acceptance: an old fixture becomes stale; missing/unrecognized data stays unknown; no quota is inferred from the session cap.

### R22. Event cursor and disconnect handling are incomplete

Location: [internal/api/server.go](../internal/api/server.go), lines 490–524.

The documented `cursor` is ignored; each connection starts with the first 200 historical events and replays forward. The handler has no read/CloseRead loop to observe socket closure when the catalog is idle. Store/read/initial write errors are also discarded.

Implement the cursor contract and bounded initial snapshot, promptly cancel disconnected clients, and surface transport errors. Acceptance: reconnection resumes correctly without duplicate history and repeated idle connect/disconnect does not leave polling handlers behind.

### R23. Several web interactions can show misleading state or fail silently

Locations: [web/src/App.tsx](../web/src/App.tsx), lines 827–857, 908–925, 1136–1154, 1519–1569, 1726–1745 and 1833–1938.

Task rows are assigned to squad members by array index rather than executor/member identity. The room uses the first running session regardless of the selected member. Returning control labels the owner `tui` but keeps the web client ID. Account rows contain a nested button. AddAccount preserves its final step/draft between openings. Several create/start/login handlers have no error display or pending protection; the squad editor can create a mission without its real objective.

Repair identity mapping, control semantics, semantic markup and dialog reset; make async errors/retries visible and prevent duplicate starts. Acceptance: add two accounts consecutively, select between two member sessions, transfer control between real clients, and exercise failed actions with clear feedback and no duplicate catalog entries.

### R24. Store migrations and catalog consistency need an upgrade contract

Location: [internal/store/store.go](../internal/store/store.go), lines 33–59; [internal/service/service.go](../internal/service/service.go), lines 184–187 and 436–439.

Migration errors from ALTER are discarded rather than distinguished from an already-existing column. Catalog writes and events are separate operations, many event errors are ignored, and tables declare no foreign keys despite enabling the pragma. There is no explicit schema version, upgrade fixture or retention policy.

Add versioned migrations and test upgrades from supported schemas; make important persistence/event failures observable and define retention. Acceptance: supported prior databases upgrade without loss, unexpected migration failures stop startup with a clear error, and mission/session references remain valid.

### R25. Documentation and localization do not match the current product

Locations: [docs/api.md](api.md), lines 42–60; [README.md](../README.md), lines 33–54; [internal/i18n/i18n.go](../internal/i18n/i18n.go); [integrations/batuta/catalog.go](../integrations/batuta/catalog.go), line 24.

API docs omit account login/workspace init and session objective behavior. The README's diagnostic fallback is not valid for a missing embed directory: `go:embed all:dist` requires matching files to compile. Many TUI texts bypass the language pack, and catalog summaries are always Portuguese. Neither platform qualification nor the exact stable core/host-plugin compatibility is documented.

Document the supported install and lifecycle, accurate API bodies/errors and known limits; localize the remaining product strings. Acceptance: English and Portuguese onboarding and docs describe the shipped behavior, including offline behavior and recovery.

### R26. Tests do not establish the complete stable workflow

Locations: [web/package.json](../web/package.json), line 5; [internal/session/manager_test.go](../internal/session/manager_test.go), lines 154–168; [internal/service/service_test.go](../internal/service/service_test.go).

There is no web test runner/script or browser end-to-end suite. Go coverage is 44.9%, with TUI 15.4% and service 48.8%. Coverage percentage alone is not a release target. `TestOnExitPersists` reads/writes its captured variable without synchronization; this is the confirmed race-detector report, not proof of a production field race. New login/init and adverse lifecycle behavior lack adequate verification.

Fix test synchronization and add tests for actual product risks: concurrent capacity, quick exits, DB contention, credentials, exact delivery/questions, restart, multi-account creation, missing CLI and partial init. Acceptance: race checks and a fresh-install end-to-end mission pass in CI on each supported platform; real host/CLI qualification complements fake process tests.

### R27. Web performance and UI maintenance can improve after correctness

Locations: [web/src/App.tsx](../web/src/App.tsx), line 81; [web/index.html](../web/index.html), line 16; [web/vite.config.ts](../web/vite.config.ts).

The main component file has 2,251 lines. The production JS chunk is approximately 680 KB (185 KB gzip), and fonts are fetched from Google. These are not the main stable-release blockers, but terminal lazy loading, separate screens and a tested offline font fallback would improve startup and maintainability.

Acceptance: extract only where it reduces change risk, measure startup before/after, and ensure the local app remains usable without external font/network access. Avoid a large visual redesign as a prerequisite to fixing R01–R20.

### R28. Existing auth-secret permissions are not enforced

Location: [internal/runtime/runtime.go](../internal/runtime/runtime.go), lines 59–76.

New secrets use mode 0600, but an existing valid synthetic secret chmodded to 0644 was loaded without correcting its permissions. Creation is also a read-then-write operation before the daemon lock, so concurrent first clients can select different tokens. A concurrent creation reproduction was not completed.

Validate/enforce existing secret permissions and serialize atomic creation. Acceptance: an existing permissive file is corrected or refused with a clear message, and simultaneous first starts agree on one token. Scope this test to disposable credentials.

## Recommended release sequence

This sequence is a proposed remediation roadmap, not an approved implementation plan. The current audit did not publish, commit, or change production code.

| Phase | Work | Exit proof |
| --- | --- | --- |
| 1. Runtime correctness | R07–R12, R28; deterministic persistence, credentials, quotas and process ownership | Race suite, concurrency/restart/child-stop fixtures pass. |
| 2. Qualified adapter and initialization | R04, R13–R15, R17; start with Codex on qualified Unix platforms | Fresh isolated official login, complete init, actual skills discovery and effective model verified. |
| 3. Mission lifecycle and evidence | R03, R05–R06, R16; plan identity/approval/core execution/exact answers/live updates | Two concurrent missions complete with distinct deliveries and an exact waiting-question round trip. |
| 4. User workflow and scope | R20, R23, R25; web onboarding, TUI role, honest capabilities | A fresh user reaches a verified result through the advertised entry point; failure/cancel/resume paths work. |
| 5. Release engineering | R01–R02, R18–R19, R24, R26; selected stable core tag and patched Go, CI and bundles | Clean CI release candidate, scan, install and runtime qualification on every supported platform. |
| Later | R21–R22, R27 and any explicitly deferred experimental feature | Recorded limits and targeted reliability/performance checks. |

A focused first stable scope is **web operation + existing-session TUI monitoring, Codex first, macOS and Linux after runtime qualification**. Claude can join when isolation/login/skills tests pass. Cursor/OpenCode/Gemini, Windows, account fallback, live provider quotas and richer diff/search views need their own qualification or explicit deferral. This is a product recommendation, not a selected release matrix.

Before implementation, settle four product choices: the stable core/host-plugin versions to coordinate; the supported adapter/platform matrix; whether the TUI must onboard users in v1; and whether quotas are imported observations or a promised real provider integration. Configure the publishing repository and choose a versioning policy for the separate Console module.

## Stable release acceptance checklist

- [ ] Install both binaries from a clean release with no sibling core or local development configuration.
- [ ] Report Console, API, core compatibility and build-toolchain versions.
- [ ] Use a patched toolchain and pass vulnerability checks with remaining findings assessed.
- [ ] Qualify official login and skills loading for every supported isolated adapter.
- [ ] Apply and display the selected model/reasoning settings; fail unavailable adapters clearly.
- [ ] Create/initialize/resume a workspace and approve a specific proposed plan through the supported UI.
- [ ] Start/resume core execution and link the mission to its exact delivery.
- [ ] Show independent status/progress/evidence for multiple missions/workspaces.
- [ ] Answer only the exact pending question and reject stale submissions.
- [ ] Update gates, attention and plans while core runs; reconnect after daemon restart.
- [ ] Keep sessions alive after client disconnect, stop their children on explicit stop, and recover catalog capacity before serving requests.
- [ ] Enforce account capacity under simultaneous starts and preserve exit state under contention.
- [ ] Pass tests, race checks and a fresh-user end-to-end mission on each shipped platform.
- [ ] Bundle current web assets, licenses, checksums, installation/upgrade notes and documented limitations.

## Limits of this review

Browser UI inspection could not run: the computer-use inventory exposed no browser. React/TUI interactions above were reviewed in source, not visually certified. Official provider authentication, real paid/model execution, Linux runtime, native folder dialogs, terminal accessibility/responsiveness and a complete approved-plan/core-loop mission were not exercised. No claim of end-to-end readiness is made from the passing build/unit tests.

At the time of this 2026-10-02 audit, Batuta initialization was pending setup/routing confirmation. On 2026-10-03, the user requested initialization and remediation planning: project profile/routing, WORK and a proposed stabilization plan were added. See [the roadmap](../.batuta/roadmap.md) and [the proposed plan](../.batuta/plans/estabilizar-console.md). The user approved the plan and initial setup commit on 2026-10-03, requiring TDD and current library releases. Product implementation and release qualification remain pending at this planning checkpoint; publication has not occurred. The historical audit findings are unchanged by initialization.
