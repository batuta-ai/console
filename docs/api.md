# HTTP and WebSocket API

Version prefix: `/v0`. Bind loopback only (`127.0.0.1`, `::1`, or `localhost`).
The daemon rejects any other listen address. Every request except
`GET /healthz` requires `Authorization: Bearer <token>`. The token is a
file under the data directory (`auth.secret`, mode 0600).

WebSocket endpoints may pass `?token=` because browsers cannot set the
Authorization header on the handshake. The server accepts origins
`127.0.0.1:*` and `localhost:*`.

## Capabilities

`GET /v0/capabilities`

```json
{
  "api": "v0",
  "product": "batuta-console",
  "integrations": ["generic", "batuta"],
  "adapters": ["codex", "claude", "opencode", "gemini"],
  "features": {
    "pty": true,
    "watch": true,
    "limits": true,
    "batuta_inject": true,
    "batuta_inject_default": true,
    "skills_catalog": true,
    "account_fallback": false,
    "folder_picker": true
  }
}
```

## Catalog

| Method | Path | Notes |
|---|---|---|
| GET, POST | `/v0/workspaces` | Create does not start sessions |
| POST | `/v0/workspaces/pick-root` | Native folder dialog; `{ "path" }` or `{ "canceled": true }` |
| GET, POST | `/v0/missions` | Create stores a resolved squad copy |
| GET, POST | `/v0/squads` | Drafts allowed. Seeded Batuta squads have `origin: "batuta"`; operator-created squads are `custom` |
| POST | `/v0/squads/{id}/validate` | No model call |
| POST | `/v0/squads/{id}/save` | Catalog version bump |
| GET | `/v0/skills` | Official Batuta skills plus extra `SKILL.md` trees under the skills root |
| GET, POST | `/v0/accounts` | References only; no secrets |
| POST | `/v0/accounts/{id}/connect` | Creates the account isolate directory (`HOME` for later sessions) and marks the catalog connected. Official CLI login happens in the session PTY, not in this request |

## Sessions and terminal

| Method | Path | Notes |
|---|---|---|
| GET, POST | `/v0/sessions` | Start a PTY. Batuta injection is on by default (`batuta` omitted or `true`). Pass `"batuta": false` for a generic CLI. Injection is a no-op if no skills directory is found (B2). GET reconciles: a catalog `running` row with no live PTY becomes `unknown` |
| POST | `/v0/sessions/{id}/stop` | Ends the process |
| POST | `/v0/sessions/{id}/control` | `{ "surface": "tui" \| "web", "client": "…" }` takes input ownership |
| GET | `/v0/sessions/{id}/terminal` | WebSocket: binary/text output; input only from the owner |

Resize JSON on the terminal socket: `{"type":"resize","cols":110,"rows":40}`.
The PTY size follows the owner. Other clients must not fight over resize.

## Watch, limits, events

| Method | Path | Notes |
|---|---|---|
| GET | `/v0/watch?workspace=` | Versioned DTO from `loop.PanelModel`; empty if no journal |
| POST | `/v0/watch/answer` | Delegates to `loop.Answer` in core; `workspace` is a catalog id (or that workspace's root). Arbitrary filesystem paths are rejected. Never writes the journal from the Console |
| GET | `/v0/limits` | Snapshots with `source`, `origin`, `last_attempt`; unknown stays unknown (percent is `null`). Collector results for the same account set are reused for 15s |
| GET | `/v0/activity` | Latest catalog/session/watch/limit events |
| GET | `/v0/events?cursor=` | WebSocket: `{type:snapshot,events}` then `{type:event,event}` diffs about every 250ms |

## Errors

JSON `{ "error": "machine_code", "message": "human" }` with 4xx/5xx.
Secrets never appear in responses, events, or logs.
