# Batuta Console

Local operator surface for agent CLIs. A per-user Go daemon owns sessions
and a versioned HTTP API; a Bubble Tea TUI and a loopback web app are
clients of that daemon. Closing a client does not end a session.

Batuta (`batuta-ai/core`) stays the authority for verified deliveries. The
Console does not require Batuta to open a CLI. A new session injects Batuta
by default (`BATUTA_SKILLS` and related env for that PTY only). Pass
`"batuta": false` for a generic CLI. Missing skills still start the session.

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

Use Go 1.27.1 or newer and run these commands from a checkout of the desired
Console revision. `go.mod` and `go.sum` pin the dependency graph, including
Batuta core `v1.1.0-beta.47`. The tracked embedded web assets are sufficient
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

The builds are written to `bin/`. Installation writes both commands to
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

## Beta infrastructure (local proposal)

Console proposes its own `v0.1.0-beta.N` sequence, independent of the linked
Core version. The manifest seed `0.1.0-beta.0` is unpublished bookkeeping;
numbering and repository destination/visibility await confirmation.

With Go 1.27.1, Node 26.10.0, npm 12.2.0 and govulncheck 1.8.0 on PATH:

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
remain pending. Native Windows portability and ConPTY/provider QA are Tasks42–44;
the current PTY start/resize implementation rejects Windows operations. WSL
proves Linux only. Other adapter catalog entries remain outside this matrix.
Sporadic WSL fork `EPERM` and stable core/plugin publication evidence remain open.
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
This endpoint supplies metadata for later clients; it adds no web/TUI flow and
does not enable blocked operations.

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

Without a built `dist`, the daemon still serves a diagnostic page so the
API can be exercised.

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

Repository documentation is English. Product UI strings (TUI and web) ship
in English with a Portuguese pack selected by `BATUTA_LANG`, then `LC_ALL`
or `LANG`. The web client reads `lang` from `GET /v0/capabilities` so it
matches the daemon, not the browser locale.
