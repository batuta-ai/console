# Dependency policy and qualification

## Policy

Use officially published releases on existing public package/module paths.
Resolve upgrades deliberately, pin direct npm dependencies exactly, and commit
`package-lock.json`, `go.mod` and `go.sum`. Verify the pinned graph with `npm ci`
and Go module checks; never use floating installs for qualification or disable
checksum, peer, type, test or vulnerability checks. Keep migrations separate
from product redesign.

Development tracks `github.com/batuta-ai/core v1.1.0-beta.47` without `replace`.
Go's `@latest` selects older stable v1.0.1; do not downgrade to it. Stable Console
publication requires a qualified stable core/plugin contract. Source checks
below do not establish release readiness or completion of the stabilization plan.

## Versions and official sources — 2026-10-03

Direct web versions were verified against the npm registry using
`npm view <package> version --json`. Independent conductor verification with
`go list -m -u -json all` found no newer target missed in the direct or consumed
Go graph. The newest published Batuta prerelease was selected explicitly.
Release and migration references: [Lyra](https://github.com/lyra-ds/lyra/releases),
[Vite](https://vite.dev/guide/migration),
[TypeScript](https://github.com/microsoft/TypeScript/releases/tag/v7.0.2),
[xterm](https://github.com/xtermjs/xterm.js/releases/tag/6.0.0),
[Bubble Tea](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.10), and
[Batuta](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.46).

| Web package | Previous resolved | Pinned |
| --- | --- | --- |
| @lyra-ds/react | 1.0.2 | 1.1.0 |
| @lyra-ds/styles | 1.0.3 | 1.1.0 |
| @xterm/addon-fit | 0.11.0 | 0.11.0 |
| @xterm/xterm | 5.5.0 | 6.0.0 |
| lucide-react | 1.46.0 | 1.51.0 |
| react, react-dom | 19.3.0 | 19.3.0 |
| @types/react, @types/react-dom | 19.3.0 | 19.3.0 |
| @vitejs/plugin-react | 4.7.0 | 6.1.1 |
| typescript | 5.9.3 | 7.0.2 |
| vite | 6.4.3 | 8.3.2 |
| vitest | 5.0.3 | 5.0.3 |
| @testing-library/react | 16.3.3 | 16.3.3 |
| @testing-library/dom | 10.4.2 | 10.4.2 |
| @testing-library/user-event | 14.6.7 | 14.6.7 |
| @testing-library/jest-dom | 7.0.1 | 7.0.1 |
| jsdom | 30.1.1 | 30.1.1 |
| @types/node | 26.6.4 | 26.6.4 |

| Direct Go module | Previous | Pinned |
| --- | --- | --- |
| charm.land/bubbletea/v2 | v2.0.9 | v2.0.10 |
| github.com/batuta-ai/core | v1.1.0-beta.46 | v1.1.0-beta.46 |
| github.com/coder/websocket | v1.8.14 | v1.8.15 |
| github.com/creack/pty | v1.1.24 | v1.1.24 |
| github.com/mattn/go-isatty | v0.0.20 | v0.0.24 |
| modernc.org/sqlite | v1.38.2 | v1.60.1 |

The complete graph is recorded in [go.mod](../go.mod), [go.sum](../go.sum) and
[package-lock.json](../web/package-lock.json). Consumed Go upgrades include
Lip Gloss v2.0.6, Ultraviolet v0.0.0-20261001125412-878653296cfd, ANSI v0.11.8,
x/sync v0.23.0, x/sys v0.48.0, libc v1.77.1 and memory v1.12.1.
Unused tooling modules are not retained as artificial application requirements.

## Engines, peers and compatibility

- Go 1.27.1, Node 26.9.0 and npm 11.19.1 were exercised on macOS arm64.
  The declared Node range `^22.22.2 || ^24.15.0 || >=26.0.0` satisfies jsdom,
  Vitest and Vite requirements; other versions in that range are not qualified.
- React 19.3.0 satisfies Lyra, React DOM, Lucide and Testing Library peers;
  React/DOM types match and Testing Library DOM 10.4.2 satisfies testing peers.
  `npm ls --all` passed. Lyra retains its upstream Lucide 1.30.0 dependency.
- Plugin React 6.1.1 requires Vite 8, which Vitest 5.0.3 supports. Published
  Babel/compiler/Oxc-transform peers are optional; no unused plugins were added.
- Lyra 1.1 changes Shell landmarks/navigation and control contrast. Existing
  wrappers, action callbacks, styles and layout remain. Dialog dismissal/focus,
  controlled tabs and workspace creation pass synthetic interaction tests.
- Vite 8 switches to Rolldown/Oxc and Lightning CSS. Existing configuration
  builds successfully; the existing large-chunk warning remains unsuppressed.
- TypeScript 7 rejects undeclared side-effect imports. The observed TS2882
  failures were resolved by retaining Vite CSS/asset types and declaring the
  stylesheet-only Lyra root with no named exports. An isolated compiler consumer
  first demonstrated arbitrary JavaScript imports were accepted, then verified
  rejection with TS2305 after the declaration correction. App typechecking passes.
- xterm 6 changes viewport/scrollbar and Alt-arrow behavior, removes deprecated
  options and moves `overviewRulerWidth`; Console does not use those options.
  Parser, text/binary output, input, resize and disposal contracts pass. The
  resize test projects `{ cols, rows }`, matching App's existing callback, and
  retains exact payload/count checks after disposal. Browser fitting is untested.
- Bubble Tea v2.0.10 fixes capability queries with disabled input; TUI rendering
  tests pass. SQLite upgrades preserve migrations and lifecycle code; existing
  store/service checks pass. Planned data/session findings remain open.

## Source qualification

Before migration, real Lyra wrapper and xterm consumption tests were added and
passed on the old graph (four files, six tests). Compiler and compatibility
failures and subsequent green results are retained in the managed run history.
No runtime source correction, new provider authentication or paid session was
needed. Synthetic tests do not qualify rendering, accessibility or real input.

The conductor independently verified candidate `88107db` on 2026-10-03:

| Portable command (repository root) | Observed result |
| --- | --- |
| `npm --prefix web ci` | Clean graph restoration passed. |
| `npm --prefix web run test:run` | Four files, six tests passed. |
| `npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json` | Passed. |
| `npm --prefix web run build` | Vite 8 production build passed; generated assets rebuilt byte-identically. |
| `go test ./...` | All 14 tested packages passed. |
| `go vet ./...` | Passed. |
| `npm --prefix web run test:journey` | Isolated real-daemon journey passed. |
| `go mod verify` | All modules verified. |
| `go build -o /tmp/console ./cmd/console` | Fresh command build passed through standalone verification. |
| `go build -o /tmp/consoled ./cmd/consoled` | Fresh daemon build passed through standalone verification. |
| `sh scripts/check-standalone.sh` | Module verification and both fresh builds/installs passed. |
| `govulncheck ./...` | Exit 0; no vulnerabilities found with Go 1.27.1. |
| `npm --prefix web audit --json` | Exit 0; zero vulnerabilities in every severity. |
| `go list -m -u -json all` | Passed; direct/consumed targets verified against live proxy metadata. |

Production builds generate `internal/webui/dist`; both binaries embed it through
`//go:embed all:dist`. Never edit those assets manually. Vulnerability results
are dated observations, not guarantees against later advisories.

## Qualification limits

Real-browser light/dark and responsive layout, focus traversal, xterm sizing,
scrolling, Alt keys and IME remain unverified; jsdom cannot establish these.
Native development watcher behavior, interactive TUI operation, official
Codex/Claude login/model execution and Linux runtime remain unqualified.
CI, binary release and the stable core/plugin publication contract need their
own qualification. No Console stable release or remote publication is claimed.

## Published Core API adoption — 2026-10-05

Console now pins the officially published
[Core v1.1.0-beta.47](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.47),
released from commit `76ecb31eaf3814e050c0e2d8733f726e991ab39a`.
The conductor verified the public Go proxy and checksum database download;
local qualification used that published module in an isolated module cache,
with `GOWORK=off`, no replacement and no sibling Core checkout. Only the Core
pin and its checksums changed; the 2026-10-03 version tables and qualification
above remain historical evidence.

The public consumption test `TestPublishedQuestionInvalidTargetDoesNotMutateJournal`
uses `loop.QuestionTarget` and `loop.AnswerQuestion` directly. It first failed
to compile against beta46 because those public symbols were absent, then passed
against beta47. Malformed delivery, task, execution, question and opening
identities return an error matching `loop.ErrInvalidQuestionTarget`, return no
delivery ID, and leave journal bytes and directory entries unchanged. The
original test-only patch and red/green logs remain intact in the archived worker
evidence; this documentation follow-up does not replace that behavioral proof.

With Go 1.27.1 on macOS arm64, `go list -m -json github.com/batuta-ai/core`
resolved exactly beta47 without a replacement and `go mod verify` passed.
The public consumption test and existing integration package tests passed.
`go test ./...` could not pass in the executor sandbox: local TCP listener
creation failed with `bind: operation not permitted` in existing HTTP/daemon
tests. Qualification stopped at that environmental boundary as required;
`go build ./cmd/console ./cmd/consoled` and `sh scripts/check-standalone.sh`
were not run by that executor. This was an environmental limitation of the
earlier submission, not the final qualification state.

The conductor independently qualified exact candidate
`fe7af126113f3ee2d7b3f711626c6069e53d7c42` on native macOS arm64 and Linux
amd64 (WSL, Ubuntu 26.04, UID 1000), using Go 1.27.1. Both passed the affected
integration tests, five race-detector iterations, `go test ./...`,
`go vet ./...`, both command builds, all 43 web tests, TypeScript checking and
the web build. Rebuilt embedded assets were byte-identical. macOS also passed
`go mod verify` and `sh scripts/check-standalone.sh`, including isolated
published beta47 source tests, builds and installs of both commands without a
sibling Core checkout.

The independent proofs are archived under the conductor repository root,
not this worktree:

- `.batuta/runs/2026-10-05-core-api-pin.green-native-mac-fe7af12.log`
  and `.json` record the macOS commands, successful exits and asset comparison.
- `.batuta/runs/2026-10-03-wsl-preflight/core-api-pin-fe7af12.log`
  records the Linux source revision, archive digest, commands and final exit 0;
  the supplied `.snapshot.json` records the archived source revision.

The conductor also reported successful ReleaseCI and release-binaries runs,
matching checksums for all five published archives against GitHub digests and
`checksums.txt`, and beta47 in the published macOS binary version. Official
Lyra npm versions remain 1.1.0. These independent results are recorded here
without rerunning the sandbox-blocked commands. The API proof establishes
consumption and invalid-target rejection without journal mutation; it does not
qualify stale-question or ownership behavior in Console.

Task19's native exact-question production integration remains pending. Console
still uses its existing legacy answer calls. Full native Linux/PTY product
qualification, stable Core/plugin publication, real provider operation and the
other runtime qualification limits above remain pending. The historical
qualification limits above describe the earlier state; the source and release
checks completed here do not establish stable release readiness.
