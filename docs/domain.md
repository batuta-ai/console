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

Isolation and native quota collection are qualified separately per adapter.
No native provider quota collector is currently qualified. Missing quota
measurements are never coerced to `0%`. Provider account quota, session
concurrency capacity (`SessionCap`), session tokens, and monetary cost are
distinct. A nonzero session capacity does not supply a quota percentage.

Each connected account gets a directory under the Console home. Session
PTYs set `HOME` (and `CODEX_HOME` / `CLAUDE_CONFIG_DIR` / `CURSOR_CONFIG_DIR`
when relevant) so two accounts of the same CLI do not share credentials.

### Imported quota observations

The limits plugin can read an externally supplied `rate_limit.json` from
the account's isolated `.codex` or `.claude` directory. This is a Console
import format, not a claim that Codex or Claude officially writes this
schema. Reading an import does not call a provider, access credentials,
or start a generation. Other adapters, including an empty adapter, Cursor,
OpenCode, and Gemini, return `unsupported`.

Imports must resolve to regular files of at most **16 KiB (16,384 bytes)**,
including JSON whitespace. This bound accommodates the small observation
schema and limits memory used for an external import. Exactly 16,384 bytes
is accepted if the contents are valid; larger files are `unknown` even when
their JSON would otherwise be valid. The read is bounded even if the file
grows during collection.

Directories, named pipes, and other nonregular files are `unknown`, including
symlinks to those file kinds. A pipe is rejected without waiting for a writer.
Symlinks to regular files are accepted under the same size and schema rules;
the opened file is checked as well as the path. Missing or unreadable regular
files remain `unavailable`. Rejected file kinds and oversized imports clear
last-good data just like invalid JSON.

An example import, using an illustrative measurement time:

```json
{
  "percent": 41,
  "window": "5h",
  "scope": "account",
  "observed_at": "2026-10-05T10:00:00Z",
  "reset_at": "2026-10-05T14:00:00Z",
  "origin": "external-exporter"
}
```

The same schema applies to both providers. `percent` is a required JSON
number in the inclusive range 0–100; an explicit `0` is a measurement,
whereas absence or `null` is not. `window` is a required nonblank string
describing the measured quota window. `scope` must be exactly `account`;
session, organization, and provider-name scopes are rejected.
`observed_at` is required, nonzero, and an RFC 3339 timestamp (fractional
seconds and timezone offsets are accepted). It must not be in the future
relative to the collection time.

`reset_at` may be absent or `null`. When provided, it must be a nonzero
RFC 3339 timestamp at or after `observed_at`. It is informational and does
not extend freshness. `origin` is an optional string naming the external
supplier. Unknown fields, invalid field types, malformed JSON, and trailing
JSON or other non-whitespace content invalidate the whole observation.

A minimal valid import is:

```json
{"percent":0,"window":"5h","scope":"account","observed_at":"2026-10-05T10:00:00Z"}
```

The example timestamps are not refreshed when the files are read. The
explicit freshness policy is **current for ages from zero up to, but not
including, five minutes; stale at five minutes or older**. This is a
Console display policy, not a provider guarantee or a quota reset schedule.

| State | Meaning |
|---|---|
| `current` | Valid import less than five minutes old; original value and measurement time retained |
| `stale` | Valid import at least five minutes old, or last good observation retained after collection becomes unavailable |
| `unknown` | Present import is invalid; no percentage, measurement time, or reset time is reported |
| `unavailable` | Supported adapter has no readable observation and no retained valid measurement |
| `unsupported` | Adapter is outside the supported Codex/Claude import contract |

Invalid imports clear the cached previous measurement. If that invalid
file later disappears, it remains unavailable instead of resurrecting an
older value. A genuinely unavailable collector may retain a previous valid
value as stale, with its original measurement time, reset time, and origin.

Imported results always identify their provenance as `imported:codex` or
`imported:claude`, optionally followed by `:<origin>` from the file.
Invalid imports also retain the provider's import marker. These markers
identify external observations, not official or independently verified
provider measurements.

`observed_at` is the supplier's measurement time. `last_attempt` is the
Console collection attempt time; neither rereading nor a cache hit replaces
the measurement time with the attempt time. Domain formatting shows the
source and provenance with the measurement time when a percentage exists,
and with the last attempt time when the measurement is unknown.

`All` caches collection results for 15 seconds for the same ordered account
IDs, adapters, and labels. File changes may take up to that interval to be
collected. Freshness is reevaluated even on cache hits, without advancing
`last_attempt`. `Snapshot` collects immediately and invalidates that batch
cache when collection completes. Last-good measurements are keyed by account
ID and adapter, so an account changing providers cannot inherit the previous
provider's quota.

Collectors run outside the plugin's shared cache lock, so a delayed collection
does not hold up other accounts. When attempts for the same account and adapter
overlap, only the most recently started attempt may update its last-good cache.
Each caller still receives its own collection result. A batch interleaved with
another completed collection is not installed in the `All` cache, preventing
a delayed batch from restoring an observation invalidated by a newer attempt.
