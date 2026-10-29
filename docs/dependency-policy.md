# Dependency policy and qualification

## Policy

Use officially published releases on existing public package/module paths.
Resolve upgrades deliberately, pin direct npm dependencies exactly, and commit
`package-lock.json`, `go.mod` and `go.sum`. Verify the pinned graph with `npm ci`
and Go module checks; never use floating installs for qualification or disable
checksum, peer, type, test or vulnerability checks. Keep migrations separate
from product redesign.

Development tracks `github.com/batuta-ai/core v1.1.0-beta.55.1` without `replace`.
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

Production builds generate `internal/webui/dist`; the daemon embeds it through
`//go:embed all:dist` and serves it to clients. Never edit those assets manually. Vulnerability results
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

## Console beta infrastructure preparation — 2026-10-06

The accepted independent Console numbering is `v0.1.0-beta.N`, with
`v0.1.0-beta.1` as the first published version and `0.1.0-beta.0` only an
unpublished manifest seed. No Console release has been published; destination
and visibility remain undefined. The current linked Core graph is beta49.
The language/check tools exercised for this preparation were Go 1.27.1, Node 26.10.0, npm 12.2.0, GoReleaser 2.18.2,
govulncheck 1.8.0 and actionlint 1.7.12. That infrastructure preparation changed
only packageManager metadata; product npm dependencies and the lockfile graph
remain unchanged. Earlier qualification tables above retain their original dates and authority.

See [release.md](release.md) for the shared checks, local snapshots, reviewed
release-PR pipeline, exact action pins and local sandbox limitations. The workflow
configuration has not run on GitHub. Current Task42 build portability is complete, while
native Windows Task43 runtime and Task44 provider qualification, full Codex/Claude
provider/web/TUI QA, Core official CLI binary security, stable Core/plugin contract
and Task37's final full release matrix remain pending. WSL qualifies Linux only.

## Current Go SDK selection — 2026-10-12

The repository is now public at [batuta-ai/console](https://github.com/batuta-ai/console), and its source-check and beta-release workflows are enabled. The release workflow inherits the organization's `RELEASE_PLEASE_TOKEN`; no credential value was copied into the repository.

The current exact SDK is Go 1.27.2, confirmed by the [official Go downloads metadata](https://go.dev/dl/?mode=json). The first public CI vulnerability scan reported 11 reachable standard-library advisories with Go 1.27.1, each fixed in Go 1.27.2. At that SDK update, Core beta49, Go module checksum and npm dependency graphs were preserved. The dated Go 1.27.1 qualification evidence above retains its original authority; source checks with the corrected SDK do not establish native Windows, provider or full release qualification.

Native mission review requires Git support for `git show-ref --exists`, which Core uses to prove exact parked-ref absence. The shared tooling check rejects unsupported versions; current qualification uses Git 2.56.0.

## Published Core approved-snapshot API adoption — 2026-10-06

At that adoption, the pin was [Core v1.1.0-beta.49](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.49),
published from `90290592cc7279c86b219a311d0ebde872fa41aa`. The public module
checksum is `h1:GnPVZNM1wbIapKJJoNDc7WQXQNpiWQIWCkkpz1yMWaE=`;
there is no replacement or private checksum-database bypass. Other dependency
pins remain unchanged. Earlier beta47 qualification above is historical.

`TestPublishedApprovedSnapshotValidation` first fails to compile on beta47,
then exercises beta49's native snapshot identity, receipt, path, size and exact
content-digest validation. It uses owned temporary source bytes and stops before
workspace preparation with an invalid delivery identity for valid snapshots;
it does not submit an executor or qualify full New/Resume lifecycle behavior.
Console does not yet call native `loop.New` or `loop.Resume`. Task21 must bind
the immutable Console receipt and snapshot to native delivery ownership.
Official Core CLI binary qualification, full provider/web/TUI operation, native
Windows and stable Core/plugin publication remain separate open gates.

## PTY compatibility exception — 2026-10-12

Console pins `github.com/creack/pty v1.1.25-0.20260601142114-9246436fffe8`
as a deliberate exception to the published-release policy. The latest official
tag, v1.1.24, still contains the wrong-slave allocation bug: an early
pointer-to-uintptr conversion can become stale when GC moves a shrinking stack.
The minimal official upstream snapshot is immutable commit
[`9246436fffe85773950f49dcc73de333da5212e6`](https://github.com/creack/pty/commit/9246436fffe85773950f49dcc73de333da5212e6),
merged through [PR215](https://github.com/creack/pty/pull/215) on June 1.
[PR216](https://github.com/creack/pty/pull/216) describes the same bug.

The exact commit was resolved through the official Go proxy and checksum
database. The module archive checksums and four affected Go files were verified
against the official commit. An independent native Linux
regression using an overlay, without product source changes, failed on v1.1.24
in 0.67 seconds: master index 6 selected `/dev/pts/0` (device 0), while both
syscalls returned 0. The red regression and official provenance are retained in
the ignored `.batuta/runs/2026-10-12-console-ci-repair` evidence directory.
The [native regression](../internal/session/pty_gc_linux_test.go) protects the
master/slave pairing under GC pressure.

Only the PTY version and checksum pair change; Go 1.27.2, Core beta49 and all
other Go and npm pins remain unchanged, with no replacement. The historical
v1.1.24 qualification and prior SDK facts above retain their dated authority.
Source compatibility checks for this snapshot include native macOS and Linux
sessions, regression tests, race checks, the full source contract, public CI and
Windows builds. Native Windows runtime and provider qualification remain
separate gates.

## Published Core beta55 adoption and native runtime qualification — 2026-10-15

The current pinned release is [Core v1.1.0-beta.55](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.55),
released from commit `5c098af1d734af1e0dd8b6733f54faa78a2536da` following normal
[PR 173](https://github.com/batuta-ai/core/pull/173). Public Core
[PR 172](https://github.com/batuta-ai/core/pull/172) merged `7985226` after CI green
and administrator authorization. Module public checksum is
`h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=` and `go.mod` checksum is
`h1:4QhaQCQhu5s44DTpi7JdQrzceq1IqTSTDkb7HQxwPCk=`. The module checksum was verified
through the public Go proxy and checksum database. In separate comparisons against
Git, all 652 module files matched release commit `5c098af1`; the 306 Go files also
matched reviewed Core `c963`. Only those Go files were compared with the reviewed
commit. No replacement directive, global cache modification or CLI upgrade was performed.
This adoption supersedes earlier current-pin wording. Dated beta46, beta47, and
beta49 evidence and tables above remain authoritative
at their recorded dates.

### Core CI and provenance limits

Core upstream CI ran clean on Linux Go 1.26.4; macOS Go 1.26.5 and Go 1.27.2 each passed
4,096 tests across 18 packages with `-race`. A known historical macOS Go 1.26.4 race timeout (15m)
cause remains unknown; newer SDK passes do not qualify older SDKs.
Core published binary archives for five targets have verified checksums matching GitHub API
digests, and metadata revision, version and platform match the release commit, but all record
`vcs.modified=true` (`v1.1.0-beta.55+dirty`). Exact build tree changes are not captured, and clean
source or reproducible build is not attested. Commit checksum and archive checksum are distinct.
These provenance limits are publicly documented in the Core release notes. Qualified stable Core
and plugin requirements remain unchanged future gates.

Current native runtime requirements supersede the earlier dated pin expectations.
On macOS and Linux, the Core CLI must be exactly `v1.1.0-beta.55` with the required
capabilities. `consoled -core /absolute/path/to/batuta` selects a private explicit
executable; otherwise `batuta` on PATH is used. Windows uses the Core SDK contained
in the Console image, with strict beta55 version, public module checksum and
no-replacement checks; it does not require an external Core CLI. Native Windows
qualification remains open. Earlier native package evidence does not qualify beta55.

### Console local bounded proof and unreleased status

Console's SDK adoption and strict native pins received review of the full change and
deltas. Bounded local checks covered service and integration tests with synthetic providers:

- 119 integration tests passed with `-race`; the unchanged concurrent-start
  regression passed all 20 executions.
- 56 Unix service-family tests passed; these do not qualify full native API/TUI operation.
- Windows strict metadata checks passed 10 subcases under an existing elevated SSH
  account only; Windows cross-build and `go vet` also passed.

These local bounded checks do not establish full Windows runtime support, real provider qualification, full web/TUI operation, or completion of Task38/43/44/Beta.
Console CI run [38754176954](https://github.com/batuta-ai/console/actions/runs/38754176954) on candidate `20ae827bbf5ebe72eb46cff986ce5956b92b7321` has finished: all 5 jobs failed. Linux and macOS native API and TUI fixtures still stamp Core 49 or lack capability response; the snapshot verifier escaped regex for 49 rejects 55; ordinary test runs are blocked by the failed producer; Windows also failed (3,530 passing and 22 failing test records, no skips), with detailed analysis pending. The four earlier Windows service failures involving Git exit 128 and Stop remain unresolved. Corrective compatibility work is ongoing.
Normal CI and bundle qualification remain open. Core publication is finished, but Console publication is not (`v0.1.0-beta.1` is not published or installed). No release-ready claim is made for Console.


## Core beta.55.1 maintenance adoption — 2026-10-29

The active dependency is [Core v1.1.0-beta.55.1](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.55.1), published from `a841b2803b68cbbf9125f3f72797f78379c89fc0` after [PR 186](https://github.com/batuta-ai/core/pull/186). This maintenance release preserves the beta.55 API and adds a clean-worktree snapshot optimization. The Go module checksum is `h1:f2QD56y4MNgW4qGrT+xtZRcK9PpWnR4j44E55uAjjO0=`; its go.mod checksum remains `h1:4QhaQCQhu5s44DTpi7JdQrzceq1IqTSTDkb7HQxwPCk=`. The official proxy and checksum database verified this module; its 309 Go files match the reviewed release source. No local replacement is accepted.

Unix CLI admission now requires exactly beta.55.1 and the required capabilities. Windows requires the embedded SDK's exact version and checksum, without replacement, alongside its existing protocol and process-identity checks. Earlier beta.55 and other dated qualification results remain historical and do not qualify beta.55.1.

The Core candidate passed 4,126 macOS Go 1.27.2 race test nodes without failure or skip, five causal mutation checks, and independent Codex review. Linux CI passed after one controlled rerun of an intermittent existing ACP cleanup test; its cause remains unknown and no ACP fix is claimed. Windows Console runtime, complete provider journeys, package qualification and Console beta publication remain separate pending gates. These checks do not change the 5-second cancellation or 2-second protocol-completion deadlines.
