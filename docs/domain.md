# Domain

The Console owns **sessions and catalog**. Batuta journals own **deliveries**.
The limits plugin owns **account usage snapshots**. None overwrites the others.

## Entities

| Object | Meaning |
|---|---|
| Workspace | Authorized project root |
| Mission | User objective, squad snapshot, history |
| Batuta delivery | Optional verified execution attached by integration |
| Task | Work item; criteria and proofs come from the integration when present |
| Squad | Versioned reusable roles, skills, and policies. `origin` is `batuta` (seeded catalog) or `custom` |
| Member | Role inside a squad |
| Skill | Named capability on a member. Official Batuta skills are listed at `GET /v0/skills`; custom names are allowed |
| Session | One agent process instance |
| Account | Named local identity for a provider (no secrets) |
| Account policy | v1 stores a fixed account on the member. Ordered fallback is not implemented |
| Evidence | Present when a Batuta journal exists; the Console does not invent it |

## Process, client, work

These are independent:

- **Process**: `running` while the PTY is alive, `exited` after stop, `unknown` after a daemon restart recovered the catalog row but not the process.
- **Client**: a TUI or web client is attached or not (`owner` is `tui`, `web`, or `none`).
- **Work**: the task may still be waiting on a human while the process is alive.

Closing a client disconnects it. Stopping a session is a separate operation.
A daemon restart recovers catalog and event history; it does not resurrect
the same PTY. The UI must label `unknown` as catalog-only, not live.

## Precedence

The rule is `workspace > mission override > member > squad default`.
Empty fields inherit from the next lower layer. A running mission keeps
the squad snapshot taken at creation; dispatch still resolves the member
against that squad. Workspace and mission override rows are not stored
yet.

Saving a squad is a catalog write. It does not start a model. Validate
probes capabilities only. The daemon seeds four Batuta squads on first
start if those ids are missing; editing them keeps `origin: batuta`.
Duplicating or creating a squad stores `origin: custom`.

A new session injects Batuta by default: `BATUTA_CONSOLE`, `BATUTA_SKILLS`,
`BATUTA_JOURNAL`, and `BATUTA_ROLE_SKILLS` from the bound member. Pass
`batuta: false` to start a generic CLI. Missing skills fall back to a
generic session (B2) instead of failing.

## Isolation and limits

Isolation and usage are `supported`, `unsupported`, or `not_yet_qualified`
per adapter. Missing measurements stay `unknown`. They are never coerced
to `0%`. The accounts table shows source, origin and last attempt alongside
the percentage when one exists. Account limit, session tokens, and monetary
cost are distinct.

Each connected account gets a directory under the Console home. Session
PTYs set `HOME` (and `CODEX_HOME` / `CLAUDE_CONFIG_DIR` when relevant) so
two Codex accounts do not share credentials. Limit collection for the same
account set is cached for 15 seconds so clients do not double the probe.
