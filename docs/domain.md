# Domain

The Console owns **catalog, sessions and immutable approval receipts**. Batuta
Core owns **native delivery execution, questions, journal and review evidence**.
The limits plugin owns **account usage observations**. A fact from one authority
must not be promoted to a claim from another.

## Entities

| Object | Meaning |
|---|---|
| Workspace | Catalog ID and authorized project root, with separately observed setup readiness and latest init attempt |
| Mission | Objective, workspace ID, squad ID and resolved squad snapshot; optional exact plan/delivery binding |
| Squad | Versioned reusable members/settings; `origin: batuta` for seeded templates, `custom` for operator-created copies |
| Member | Stable member ID, role, CLI, fixed account, model, effort, skills and Maestro selection |
| Skill | Named capability; official catalog entries remain visible without installation, custom disk summaries are product text |
| Account | Named local provider identity and private-home reference; no secrets in the catalog |
| Session | One daemon-managed PTY process identity, distinct from a native delivery |
| Plan identity | Mission plus full workspace/plan binding and exact-byte SHA-256 version |
| Approval | Immutable Console receipt of explicitly approved original bytes; not execution, source status or task completion |
| Delivery | Core-owned execution with exact opening identity and optional Console approval reference |
| Question | Current seven-field native identity and prompt, not merely a string to answer |
| Evidence | Verified native projection/review facts; never invented from logs or catalog counters |

## Account and workspace readiness

Creating an account, connecting its isolated HOME, completing official CLI login
and observing authenticated readiness are separate steps. Login uses the selected
account's private HOME after CLI compatibility checks. Official init, planning
and native execution require fresh readiness; no global/personal credential
fallback authorizes them. The current supported operational pairs are Codex and
Claude on macOS/Linux. Native Windows is intended scope with qualification still
pending; WSL qualifies Linux only. A static isolation label or PATH lookup does
not qualify a provider/platform.

Workspace registration does not initialize the project. Init requires an explicit
account and eight confirmed configuration fields plus optional notes. The actual
init session, stop intent, process state and configuration on disk are independent.
An identical live attempt can be reused; conflicting inputs or already complete
configuration reject another start. `setup.ready` is configuration evidence,
not proof that a process exited successfully. Unknown/interrupted attempts require
inspection before another explicit action.

## Process, client and work

These are independent:

- **Process:** `running` while owned live, `exited` after observed termination,
  `unknown` when catalog identity cannot be reconciled to a live owned process.
- **Client:** attached or disconnected; input ownership is separately represented
  by `owner` (`tui`, `web`, `none`) and `owner_client`.
- **Work:** interactive planning, login or init can coexist with a mission waiting
  for a human. Native execution has its own daemon job and journal authority.

Closing or detaching a client does not stop its session. Taking input ownership,
attaching a terminal and stopping a process are separate explicit actions.
Ownership gates input and resize; release requires the current owner client.
Stop acts on a daemon-owned process, not an inferred PID from recovered history.
A daemon restart recovers catalog/events but does not resurrect a lost PTY;
`unknown` is catalog-only and cannot be reattached. Native recovery validates the
original approval/opening and resumes explicitly. `active=false` is not Done.

## Snapshot selection and catalog precedence

The binding helper resolves `workspace > mission override > member > squad
default`; empty fields inherit. Workspace/mission override rows are not stored
by the current editor. Mission creation stores a resolved copy of the chosen
squad. Current squad edits do not rewrite that snapshot; session dispatch uses
its selected member, with the current same-ID squad member as the lower-priority
fallback for empty fields. Clients present actual snapshot member/account/provider
identities, never an unrelated first/global session as the recipient.

Saving a squad is a catalog write, not model execution. Structural draft state
is durable; fresh readiness is separate. Validation probes structure/readiness,
not generation. A blank draft can fall back to stored-squad validation; inspect
the returned identity rather than treating a generic refusal as a draft verdict.
The daemon seeds four Batuta templates when their IDs are absent. Vacant account
bindings can be filled only for a unique connected account of the matching CLI.
Editing a template keeps its origin; creating/duplicating makes a custom squad.

Language selection changes product scaffolding and official/custom skill
summaries. It never rewrites stored names/purposes/roles, even on seeded templates,
member IDs, account bindings, versions, user objectives/answers, provider output,
receipt bytes or evidence provenance. Installation status/path and native role
selection are independent of summary language.

## Planning and skill preparation

Interactive planning targets the mission's one selected snapshot Maestro.
Its goal is supplied in CLI launch arguments, with model/effort from bindings;
there is no automatically typed terminal command, implicit Take/Stop, approval
or native Start. Clients inspect and explicitly attach the returned session.
The `batuta` request flag defaults to true. False disables injection, not
readiness/isolation checks for official starts.

Preparation supplies session-local `BATUTA_CONSOLE`, `BATUTA_WORKSPACE`, and,
when enabled, `BATUTA_SKILLS` and `BATUTA_JOURNAL`; member skills and mission
text also supply `BATUTA_ROLE_SKILLS`/`BATUTA_MISSION`. Selected installed skills
are linked into the selected account's native discovery directories. Conflicting
or unselected destinations preserve existing content and refuse launch. Custom
catalog visibility does not authorize loading that skill: roles select it by
exact name. The low-level preparation helper can disable injection when no root
exists, but missing named roles or required `batuta-plan`/`batuta-init` still
reject the operation. It is not a universal generic-session fallback.

## Plan, approval and native recovery

Binding associates a mission with one exact plan path/task digest and optionally
a verified journal opening. It does not write source or journal. Mission watch
never adopts a nearby/latest delivery. Plan-only binding can be changed explicitly;
a delivery-bound association rejects replacement. Missing/invalid/foreign artifacts
remain unavailable rather than silently changing identity.

Before native binding, Plan Detail is the current exact editable source. Its
`version` hashes every byte; `plan_digest` identifies Core's task set. Content is
base64 on the wire so line endings and invalid UTF-8 survive unchanged. Explicit
approval validates the shown version and records immutable original bytes. It
neither marks a source/task complete nor invokes a model. Initial Start requires
that exact receipt and an unchanged eligible source.

The approved **base target** retains empty binding delivery/opening fields.
Native Start uses Core's approved snapshot support and a deterministic delivery;
its actual `delivery` and `opened_digest` are separate returned identities. The
Console validates account/model/effort routes and their selected skills, shared
capacity, readiness and a matching Core CLI. Core performs execution and supervised
review; the Console does not fabricate a journal or edit source to mark progress.

After native delivery binding, Plan Detail returns the **original approved
snapshot and receipt identity**, independent of edited/deleted source. Current
editable bytes are unavailable through this API. Historical receipt reads remain
independent of today's source, but alone do not authorize Start. Resume retains
the original receipt, base target, actual delivery and original opening. It does
not create a new receipt/delivery or substitute the current file. A native-bound
mission should be inspected and explicitly resumed, not rebound/reapproved as a
retry shortcut. Externally bound journals without a native approval reference
continue to require their own exact artifact validation.

A question's identity is `mission_id`, `workspace_id`, `delivery`, `task`,
`execution`, `question_id`, `opened_digest`. The execution number distinguishes
continuations even if the prompt/question digest repeats. Answer revalidates the
current identity through Core and records text on the same explicit delivery;
it does not automatically resume. If a catalog event fails after the journal
answer was recorded, the recorded effect is retained even on an error response.
The TUI retains old drafts under `j`; `n` explicitly confirms replacing one with
a new draft for the current question.

Done requires current matching projection and a cleared review gate backed by
review/evidence identity. Process exit, inactive jobs, catalog `status`, attention,
task counts, tokens and logs cannot substitute. A stale projection must be refreshed
before authorizing a new action.

## Rejected and uncertain operations

Requests carry exact catalog/native identities. A verified rejection is different
from an uncertain result after dispatch. Lost replies, malformed/foreign response
identities, cancellation and store failures can leave effects that are not yet
confirmed. Retain confirmed facts and the submitted target/draft; quarantine replay
and explicitly inspect the actual session, receipt or original native delivery.
Read refresh/acknowledgment is not mutation success. A retry control is not evidence
that redispatch is safe. Reconnect restores observation and never replays input or
a mission mutation automatically.

Component/source checks are separate from full operational qualification. Remaining
release gates include native Windows/process/isolation/provider proof, complete
web/TUI/provider journeys, reliability findings, stable Core/plugin compatibility
and official Core binary security. Build/runtime labels do not close those gates.
Console's first planned publication is `v0.1.0-beta.1`; unpublished `beta.0`
bookkeeping and linked Core `v1.1.0-beta.49` are independent. No Console publication
or successful public CI is established here; see [release authorities](release.md).

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
