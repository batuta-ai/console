# Batuta Console

Local operator surface for agent CLIs. A per-user Go daemon owns sessions
and a versioned HTTP API; a Bubble Tea TUI and a loopback web app are
clients of that daemon. Closing a client does not end a session.

Batuta (`batuta-ai/core`) stays the authority for verified deliveries. The
Console does not require Batuta to open a CLI. A new session injects Batuta
by default (`BATUTA_SKILLS` and related env for that PTY only). Pass
`"batuta": false` for a generic CLI. Missing skills still start the session.

This repository is a sibling of [`batuta-ai/core`](https://github.com/batuta-ai/core).
It is not part of the core module (core stays daemon-free) and not part of
the host plugin package.

## Commands

```
consoled                         start the local daemon
console                          start the daemon if needed, then the TUI
console --web                    print the local web URL
```

Listen address defaults to `127.0.0.1:9477`. State lives under
`$BATUTA_CONSOLE_HOME` or `~/.local/state/batuta/console`.

## Develop

```bash
go test ./...
go build -o bin/consoled ./cmd/consoled
go build -o bin/console ./cmd/console
```

The web UI lives in `web/` and is embedded from `internal/webui/dist`:

```bash
cd web && npm install && npm run build
go build -o bin/consoled ./cmd/consoled
```

Without a built `dist`, the daemon still serves a diagnostic page so the
API can be exercised.

Account connect creates an isolated HOME for that catalog row. Official
CLI login happens later in the session PTY using that HOME — the API
never accepts a secret.

## Documentation

- [Domain](docs/domain.md)
- [HTTP and WebSocket API](docs/api.md)

Repository documentation is English. Product UI strings ship in English
with a Portuguese pack selected by `BATUTA_LANG` or `LANG`.
