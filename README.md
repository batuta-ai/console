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

Use Go 1.26.4 or newer and run these commands from a checkout of the desired
Console revision. `go.mod` and `go.sum` pin the dependency graph, including
Batuta core `v1.1.0-beta.46`. The tracked embedded web assets are sufficient
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
