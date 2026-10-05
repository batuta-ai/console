# Profile — Batuta Console

Stack: Go 1.27.1 daemon/client + React 19.3, TypeScript 7.0.2, Vite 8.3.2, Bubble Tea 2.0.10 TUI, SQLite 1.60.1, xterm 6; Lyra React/styles 1.1.0 and existing CSS.
Methodology: TDD always: write the behavioral/regression test first, run it and observe the expected failure, implement the smallest correction, rerun to green, then refactor; retain red/green commands and output. Conventional commits; feature branches; keep existing architecture and styling/state conventions.
Test: go test ./... && npm --prefix web run test:run && npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json
Build: npm --prefix web run build && go build -o bin/console ./cmd/console && go build -o bin/consoled ./cmd/consoled
Install: go mod download && npm --prefix web ci
Execution: sequential
Worktree: medium+
Template: templates/react.md
Dependencies: Use the latest published releases verified against official registries/release notes at implementation time, including Lyra and Batuta. Pin the resolved dependency graph and run compatibility/regression checks. Batuta development may track its latest pre-release, while stable publication requires a qualified stable core/plugin contract. Do not silently downgrade to an older release or install floating latest dependencies during verification.
Product scope: Codex and Claude; macOS and Linux; full operation in the web panel and TUI, selected by the user on 2026-10-03.

Configured on 2026-10-03 using the defaults proposed during the audit and the user's instruction to start Batuta init. Test/build commands above describe the current repository. Add product-risk tests and release checks through the approved plan; the source vulnerability scans are clean after tasks 2/4; the admission, exit-ordering and SQLite race regressions passed host checks in tasks 6/7; full runtime release qualification remains pending. Format Go changes with gofmt. Public product documentation follows existing English docs; conducting records are in Portuguese and product strings support PT/EN.

## Project map

This map was first verified against Console HEAD 0461003 on 2026-10-03; dependency and test entries were refreshed after tasks 1–4. Start from source, not generated assets.
The root go.mod/go.sum define the Go module and pin published core v1.1.0-beta.47; there is no local core replacement. scripts/check-standalone.sh verifies an isolated test/build/install of both commands.
cmd/console/main.go is the user CLI entry point and selects web or Bubble Tea mode.
cmd/console/ensure.go starts/finds consoled; daemon/open platform files handle process detachment and browser launching.
cmd/consoled/main.go wires runtime paths, store, sessions, limits, service and HTTP server.
internal/runtime/runtime.go owns paths, the local auth secret and daemon locking.
internal/api/server.go defines HTTP and WebSocket routes, capabilities and authenticated transport.
internal/service/service.go coordinates catalog operations, login, workspace init, sessions and mission actions.
internal/store/store.go owns SQLite schema, queries, writes and catalog events; upgrade and contention tests live beside it.
internal/domain/domain.go contains persisted entities and binding logic; view.go contains frontend/watch DTOs.
internal/session/manager.go owns live process/PTY sessions and OnExit behavior.
internal/session/pty_unix.go implements PTYs; pty_windows.go currently reports unsupported startup.
internal/adapters/adapters.go defines CLI availability, authentication checks and isolated adapter configuration.
internal/accounts/accounts.go builds isolated account HOME/config environments.
integrations/batuta/inject.go locates/injects skills; plans.go, watch.go and catalog.go bridge core plans, journal and catalog.
plugins/limits/disk.go reads imported provider observations; limits.go produces the account limit view.
internal/client/client.go is the daemon HTTP/socket client; term.go handles terminal transport.
internal/tui/tui.go is the Bubble Tea model/view; pty.go and glyphs.go support terminal output and rendering.
internal/i18n/i18n.go defines Go-side language packs, with tests next to it.
internal/hostfs/pick.go provides native directory selection; OS runtime behavior needs qualification beyond compilation.
web/src/main.tsx mounts React; App.tsx currently holds navigation and workspace/account/squad/mission/terminal flows.
web/src/lib.ts contains API types, requests, authentication and sockets; i18n.ts holds web translations.
web/src/ui.tsx wraps Lyra primitives; app.css and batuta-theme.css contain application and theme styling.
web/package.json/package-lock.json pin the verified frontend graph and npm scripts; tsconfig.json checks web/src and vite-env.d.ts provides CSS-only import contracts. docs/dependency-policy.md records current versions, source qualification and limits.
web/vite.config.ts emits the production frontend into internal/webui/dist.
internal/webui/embed.go embeds all:dist; that tracked directory must be regenerated by the web build, never edited manually.
Go tests are colocated *_test.go files; web/vitest.config.ts, App/lib tests and test setup cover web interactions; web/tests/daemon-journey.mjs qualifies an isolated real daemon without provider calls.
README.md and docs/api.md/docs/domain.md describe the product; docs/release-readiness*.md contain the audit and evidence.
.batuta/profile.md, routing.md, roadmap.md, plans/ and WORK.md are conducting state, not the product's execution catalog.
Do not edit node_modules, binaries, SQLite runtime files or .batuta/runs/scout/worktrees as source; do not modify sibling core from a Console task.
