# HTTP and WebSocket API

The daemon exposes API `/v0` on loopback only (`127.0.0.1`, `::1` or
`localhost`). `GET /healthz` is unauthenticated. API requests require
`Authorization: Bearer <token>` from the selected Console home's `auth.secret`
(mode 0600). Static web assets are served separately. Use `console --web` to
open the UI with that home's token; a token from another home is not interchangeable.

Terminal and event WebSocket handshakes also accept `?token=`. Their origin
patterns are `127.0.0.1:*` and `localhost:*`; they are not a remote service.
Identifiers below are actual returned catalog IDs, not labels or array positions.
The typed Go client lives in `internal/client`; wire types are in
`internal/domain` and `internal/client/resources.go`.

## Capabilities and language

`GET /v0/capabilities` returns `api`, `product`, `lang`, `integrations`,
`adapters`, `features`, `runtime`, `provenance`, `matrix`, `pty` and `providers`.
`api` is `v0`, `product` is `batuta-console`; integrations are `generic` and
`batuta`. Adapter catalog entries include providers outside the enabled matrix.
Feature keys are `pty`, `watch`, `limits`, `batuta_inject`,
`batuta_inject_default`, `skills_catalog`, `account_fallback` and `folder_picker`.
`account_fallback` is false. Feature presence is not an operation authorization.

`lang` is `en` or `pt`: explicit `BATUTA_LANG` wins (a `pt` prefix selects
Portuguese; any other nonempty value selects English); otherwise a Portuguese
`LC_ALL` or `LANG` selects Portuguese, default English. The web uses this daemon
value, not browser locale. Skill summaries use the same language. Stored user
and squad text, provider output, identifiers and original artifact bytes are
not translated.

`matrix` currently reports `darwin`/`linux`, `codex`/`claude` and qualification
`pending`; the intended product scope also includes native Windows, whose
portability, PTY/process/isolation and provider qualification remain pending.
WSL is Linux evidence only. `runtime` and `provenance` report the running
binary's observed build/toolchain/module facts, not release qualification.
`pty.supported` identifies the selected backend; `pty.available` and legacy
`features.pty` reflect a request-local allocation/resize/close probe with no
process launch. Provider installation is PATH presence, not compatibility or
authentication. Capabilities does not execute a CLI or read account credentials.
See [README matrix](../README.md#selected-platform-and-provider-matrix) and
[version authorities](release.md) for fields and qualification limits.

## Accounts, workspaces and catalog

Unless noted, successful JSON operations return HTTP 200.

| Method | Path | Request / result and effect |
|---|---|---|
| GET | `/v0/accounts` | Account list with fresh `readiness`; bounded CLI/authentication checks, not model generation |
| POST | `/v0/accounts` | `{label, adapter}` → account; no secret input |
| PATCH | `/v0/accounts/{id}` | `{label}` → same account identity/provider/isolation |
| POST | `/v0/accounts/{id}/connect` | No body required; prepares private HOME and returns decorated account; does not perform official login |
| POST | `/v0/accounts/{id}/login` | `{owner, client}` → actual login `Session`; may reuse a live login for this exact account |
| GET | `/v0/workspaces` | Decorated workspace list with `profile` and `setup` observations |
| POST | `/v0/workspaces` | `{name, root}` → registered workspace; existing absolute root can return its existing identity; no init |
| POST | `/v0/workspaces/pick-root` | Native picker → `{path}` or `{canceled:true}`; no registration |
| POST | `/v0/workspaces/{id}/init` | `{account_id, config, brief, owner, client}` → exact init `Session` |
| GET | `/v0/squads` | Versioned catalog, including stored templates |
| POST | `/v0/squads` | `Squad` draft → saved squad, generated missing IDs/version update; structural draft state allowed |
| POST | `/v0/squads/{id}/save` | Saves the existing stored squad again, bumping its version; not an arbitrary draft update |
| POST | `/v0/squads/{id}/validate` | Optional draft → `{validation, squad}`; structure/readiness checks, no save or model generation |
| GET | `/v0/skills` | `{root, inject_default, skills}`; each skill has `id`, `name`, `origin`, `summary`, `installed`, optional `path` |
| GET | `/v0/missions?workspace={id}` | Mission catalog, optionally filtered by workspace ID |
| POST | `/v0/missions` | `{workspace_id, title, brief, squad_id}` → mission with resolved squad snapshot; no binding, approval or execution |

`config` contains exactly the setup fields `stack`, `methodology`, `test`,
`build`, `install`, `execution`, `worktree`, `template`. Each is required,
trimmed, valid UTF-8, at most 8192 bytes and free of NUL/CR/LF. Execution accepts
`sequential`/`parallel`; Worktree accepts `off`/`medium+`/`always`. `brief` is
optional notes, at most 65536 UTF-8 bytes without NUL. Init requires an explicit
ready account; it passes confirmed literal answers to the official CLI. An
identical live account/argv attempt may be reused; conflicting live inputs or
already complete configuration reject a new attempt (409 `init`).

Workspace `setup` has `ready`, `state`, optional `code`,
`configuration_code` and `attempt`. The attempt includes `session_id`,
`account_id`, `state`, `process`, `stop_requested`, optional `recovery_reason`,
`created_at` and optional `ended_at`. Configuration and process/stop intent are
separate. A cancelled or exited attempt is not proof of ready configuration.

Account readiness distinguishes `supported`, `available`, `cli_compatible`,
`connected`, `credential_configured`, `authenticated`, `ready`, `code`,
`checked_at` and optional `credential_source`. Login checks CLI compatibility
so it can repair credentials; init and official interactive/native execution
require fresh authentication. Checks use bounded five-second contexts. A
credential file or a static isolation label alone is insufficient. No operation
silently uses personal/global credentials or an arbitrary first account.

An empty validation body, or a draft with both empty name and no members, uses
the stored squad. Consumers must inspect the returned squad identity; a generic
validation refusal is not proof that the current draft was validated. The
catalog may bind vacant members only when a unique connected account exists for
that CLI; clients still review actual IDs and fresh readiness. Saving/validation
never dispatches a model. Listing skills does not install or authorize them.

## Plan binding, exact detail and immutable approval

| Method | Path | Contract |
|---|---|---|
| PUT | `/v0/missions/{id}/binding` | `{workspace_id, plan_path, delivery_id?}` → Mission |
| GET | `/v0/missions/{id}/watch` | Mission-scoped projection; optional `workspace`/`delivery` must match |
| GET | `/v0/watch?mission={id}&workspace={id}&delivery={id}` | Equivalent mission watch selector |
| GET | `/v0/missions/{id}/plan?workspace_id={id}` | `PlanDetail` |
| POST | `/v0/missions/{id}/plan/approvals` | Exact `PlanIdentity` body → `PlanApprovalResult` |
| GET | `/v0/missions/{id}/plan/approvals/{receipt}?workspace_id={id}` | Historical immutable `PlanApproval`, scoped to mission/workspace |

Binding requires an exact relative plan path under the authorized workspace.
It validates the parsed plan and optional journal opening; it does not create
or edit the plan/journal. Plan-only binding can be replaced explicitly; once a
delivery is bound, only the same association is accepted. No nearby-plan,
latest-delivery, workspace-discovery or filename fallback authorizes a mission.
Invalid, ambiguous, symlinked or foreign artifacts are rejected.

`MissionBinding` contains `workspace_id`, `workspace_root`, `canonical_root`,
`plan_path`, `plan_slug`, `plan_digest`, and optional `delivery_id` and
`opened_digest` (the latter two form a pair). `PlanIdentity` contains
`mission_id`, that complete `binding`, and `version`: lowercase hexadecimal
SHA-256 of **every original byte**. `plan_digest` is Core's task-set identity;
it is not this byte-level version.

`PlanDetail` contains `identity`, base64 `content` (Go `[]byte`, preserving
invalid UTF-8 and line endings), `title`, `goal`, `context`, `created`, `inputs`,
`status`, `tasks`, and optional `approval`. Each task includes `id`, `number`,
`title`, `status`, `domain`, `complexity`, `dependencies`, `executor`, `model`,
`scope`, `accept`, `line`. The plan byte limit is 1 MiB.

**Before native delivery binding**, detail reads the current exact editable
source. Approval verifies its current binding and byte version, accepts eligible
`proposed`/`approved` plans outside `/done/`, and records a receipt. It changes
neither source status nor task completion and starts no model. The receipt has
`id`, `identity`, base64 `content` and `approved_at`. Historical retrieval does
not require today's file/binding to match and does not grant new execution authority.

**After native delivery binding**, detail validates the journal's approval
reference and returns the **original approved receipt/content/identity**. Edited
or deleted source does not replace it. These are original snapshot bytes, not
current-file bytes; this API has no local-file fallback for current editable
content. New approval is not the recovery mechanism for a native-bound mission.
An externally bound delivery without a native receipt retains the exact-source
validation path rather than gaining native receipt semantics.

Plan detail/receipt GETs accept exactly one nonempty `workspace_id` query key.
Approval accepts no query, requires body mission ID to match the path, disallows
unknown fields/trailing JSON and caps the request at 16 KiB (binding: 4096 bytes).
`PlanApprovalResult` is `{target, effect, receipt?, error?}` with effect
`recorded`, `rejected` or `uncertain`; nested error is `{code,message}`.
Recorded is HTTP 200; uncertain is 503 `plan_uncertain` and requires inspection.

## Native execution and original resume

| Method | Path | Request / result |
|---|---|---|
| POST | `/v0/missions/{id}/execution` | `{target: PlanIdentity, approval_id}` → `ExecutionResult` |
| GET | `/v0/missions/{id}/execution?workspace_id={id}&approval_id={receipt}` | `ExecutionState`; exactly these two nonempty query keys |
| POST | `/v0/missions/{id}/execution/resume` | `{target: PlanIdentity, approval_id, delivery, opened_digest}` → `ExecutionResult` |
| POST | `/v0/missions/{id}/execution/stop` | `{target: PlanIdentity, approval_id, delivery, run_id}` → `ExecutionStopResult` |

Start uses the original approved **base target**: its binding has empty
`delivery_id`/`opened_digest`. Do not patch the delivery back into that target.
Start verifies the unchanged source and receipt, selected snapshot account/routes,
fresh readiness, installed skills, shared capacity and the matching Core CLI.
The service uses Core `loop.New`/`Run` with `ApprovedSnapshot`; it does not
synthesize journal records. Native work is daemon-owned and survives client
request cancellation; shutdown drains owned work separately from HTTP clients.

`ExecutionResult` echoes `target`, `approval_id`, `effect`, optional `delivery`,
`opened_digest`, `run_id`, `error:{code,message}`. `effect` is `started`, `rejected` or
`uncertain`. HTTP 202 with `started` confirms the native delivery/opening, not
completion. Delivery is deterministic from approval ID, mission ID and workspace
ID. It is separate from the original target. An existing native delivery must
be resumed explicitly; a lost reply is not permission to Start again.

`ExecutionState` contains `target`, `approval_id`, `delivery`, optional
`opened_digest`, `run_id`, `active`, `bound`, optional `watch` and `gate`.
Before admission it can report the deterministic delivery with `bound=false`,
`active=false` and no watch/opening or current `run_id`. Admitted preparation
before opening is already `active=true` and includes `run_id`, even while
`bound=false`. A parked question without an active admission has no live native
process to stop.
`active` means this daemon owns an admitted native job. `bound` means catalog
association. Neither is a completion or provider-success claim.

Resume requires the **original** receipt/target plus the exact actual delivery
and original opening digest. The service validates the approved journal and uses
Core `loop.Resume`; it does not substitute edited source, a fresh receipt or a
new delivery. GET state may reveal an opening after an uncertain mutation even
when catalog binding has not yet been confirmed. Preserve these facts and
inspect authoritatively before explicitly resuming; never infer success from a
list refresh or replay a pending mutation automatically.

The gate has `required`, `cleared`, `delivery`, optional `review_id`,
`evidence_digest`, `reason`. Completion requires matching current projection
and cleared review/evidence authority. Exit, absence of active work, catalog
status/counters and attention are not sufficient. See [domain](domain.md).
Start/resume enforce exact mission identity, no query, strict JSON and 16 KiB
request limits. Rejected/uncertain effects remain distinct even on HTTP errors.

### Stop one exact native attempt

Stop uses the existing local bearer token and the original approved base `target`
(with empty binding `delivery_id`/`opened_digest`), exact `approval_id`, separate
execution `delivery`, and `run_id`. No query is accepted. The body is limited to
16 KiB; unknown fields, trailing JSON, a target mission different from the URL,
and invalid references are rejected with HTTP 400 `execution_reference`.

The server generates `run_id` from 32 random bytes, encoded as 64 lowercase
hexadecimal characters, for each admission. It appears in active/preparing
`ExecutionState` and a `started` or `uncertain` `ExecutionResult`. Explicit resume
admits a new attempt with a fresh value. Daemon recreation invalidates old admission nonces; it does
not admit work or produce a new `run_id`. A later explicit Start/Resume admission
does that. Inactive state has no current `run_id`. Never substitute a PID,
process name, session ID, latest delivery or reconstructed newer `run_id`.

`ExecutionStopResult` echoes the four request fields and includes `effect`,
`finalized`, `durable`, optional `core_state`, `state`, `causes` (error strings),
and `error:{code,message}`:

| HTTP / effect | Meaning |
|---|---|
| 202 `pending` | Cancellation accepted; owned work, resources, catalog and journal finalization remain outstanding. `finalized=false`, `durable=false`; no terminal or durable success is established. |
| 200 `completed` | Full finalization and the currently qualified durable outcome are verified: `finalized=true`, `durable=true`, with `state`. Read the actual `core_state`; completion does not mean task success or automatically mean canceled. |
| 503 `uncertain` | Finalization finished but requires inspection: `finalized=true`, with `execution_stop_uncertain`, available state and actual causes. `durable` may still be true when an independent run/cleanup error or unresolved review remains. |
| Error / `rejected` | Reference, authority or validation failed, the native manager was unavailable (503 `execution_unavailable`), or the request was canceled (408 `execution_canceled`) before cancellation was accepted; `finalized=false`, `durable=false`. Authentication is handled separately below. |

An opened delivery can report `done`, `blocked`, `waiting_input`, `waiting_plan`,
`canceled` or `abandoned`; a completed Stop before opening can omit `core_state`.
Persistence, ownership, cleanup, release, review and independent resource errors
must remain visible rather than being hidden as expected cancellation.

Stop error paths include 400 `plan_reference` for unknown/foreign/nonretained
admission references, 409 `plan_binding` for binding conflicts, 503
`execution_unavailable`, and 408 `execution_canceled` for request cancellation
before acceptance. Receipt/canonical validation also uses the common plan errors
below, including 404 `plan_missing`, 422 `plan_artifact`, 503 `plan_io` and 500
`plan_store`. This is not an exhaustive promise about underlying validation errors.
Missing/invalid bearer credentials return middleware HTTP 401 `unauthorized` in
the generic error body, before the `ExecutionStopResult` handler.

A disconnected or timed-out caller does not revoke accepted Stop or own its
finalization. Inspect a lost reply; never automatically replay it. Explicit
reobservation sends the exact same four fields. Up to 64 historical outcomes
are retained in daemon memory: a server-validated old exact reference may return
only its cached result without canceling a replacement. These observations are
neither durable invocation IDs nor cancellation authority. After daemon
recreation, recover the real receipt, delivery, opening and journal state;
never treat the former nonce as current.

### Current read availability

GET execution state can return HTTP 503 `plan_io` when fresh progress/review-gate
facts cannot be verified. This means unavailable, not Done or mutation authority;
it can reflect persistent I/O or corruption as well as contention. Manually
reinspect until fresh verified facts are available. A last verified exact Stop
reference remains a candidate through a later read failure, but every Stop,
including historical reobservation, still validates receipt, binding and canonical
workspace authority on the server. This grants no freshness to Start, Resume,
Answer or approval. Completing an old Stop cannot clear another mutation's
uncertainty. Local failures before dispatch allow explicit recovery; a lost reply
after dispatch does not automatically restore send confirmation.

## Exact current question answers

`POST /v0/watch/answer` takes the seven fields from
`WatchView.pending_question.target`: `mission_id`, `workspace_id`, `delivery`,
`task`, `execution` (integer), `question_id`, `opened_digest`, plus `text`.
The optional legacy `workspace` field cannot authorize an answer; if present
it must equal `workspace_id`, not a filesystem root. Strict JSON, no trailing
value, request limit 1 MiB. Never rebuild identity from the prompt text.

The service revalidates the mission, workspace, opening and current question,
then delegates to Core `loop.AnswerQuestion`. The result is
`{target, delivery?, effect, error?}`. `recorded` confirms the answer in the
same explicit delivery; **it does not resume execution**. A subsequent explicit
Resume uses the original receipt/delivery/opening. `rejected` means the target
was refused; `uncertain` requires inspection without replay. A recorded answer
can still return HTTP 500 `answer_recorded_error` if the catalog event failed:
retain the confirmed recorded effect instead of sending the answer again.
Stale/foreign questions preserve the draft; a new current question needs an
explicitly reviewed new draft rather than recycling the previous identity.

## Sessions, ownership and terminal transport

| Method | Path | Contract |
|---|---|---|
| GET | `/v0/sessions` | Reconciles catalog rows; missing live PTYs become `unknown` |
| POST | `/v0/sessions` | `{mission_id, member_id?, account_id?, adapter, argv?, batuta, owner?, client?}` → actual `Session` |
| POST | `/v0/sessions/{id}/control` | `{surface:"tui"\|"web"\|"none", client}` takes/releases input ownership |
| POST | `/v0/sessions/{id}/stop` | Explicitly stops a daemon-owned process; not task completion |
| GET | `/v0/sessions/{id}/terminal?client={client}` | Authenticated WebSocket observation/input for that exact session |

Clients select the mission snapshot member and its actual matching account/provider.
Omitting `batuta` requests injection; false disables it, not official readiness
checks. Model/effort come from resolved member bindings. The explicit custom
`argv` path is distinct from official interactive starts and rejects configured
model/effort; it is not a personal-credential fallback. Missing named planning,
init or role skills and conflicting account discovery links can reject launch.
The low-level preparation helper's disabled-injection result is not a universal
promise that missing skills will start a generic session.

Process (`running`/`exited`/`unknown`), client attachment, ownership (`owner`,
`owner_client`) and work are independent. Observing/attaching does not take
control; taking control does not attach. Release with `surface:"none"` requires
the current owner client; wrong-owner release is 403.

`POST /v0/sessions/{id}/stop` passes the request context to `StopSessionContext`.
For owned sessions, HTTP 200 is returned only after actual required durable
completion (both native resource cleanup and atomic `session.stopped` catalog
persistence). It requires a live session owned by this daemon, not terminal
input ownership:

- Unknown/catalog-only sessions return HTTP 409 (`error:"session_not_owned"`),
  except an already `exited` row which returns HTTP 200 idempotently without
  emitting new events.
- A missing session ID returns HTTP 404 (`error:"not_found"`).
- Requests lacking authentication return HTTP 401 (`error:"unauthorized"`).
- When client request cancellation occurs while full durable finalization is
  still pending after the native-stop phase has returned without truncating
  existing budgets, the handler checks for `FinalizationPendingError` before any
  generic 500 and returns HTTP 503 with `{"error":"stop_pending","message":...}`
  (joining any known native cause in the message). A 503 `stop_pending` response
  is neither terminal failure proof nor a completed stop; work remains tracked
  in the background through daemon shutdown, but pending does not imply eventual
  success. Clients must not assume automatic retry or replay.
- A request already pre-canceled before the handler begins initiates no new stop
  signaling or catalog mutation. With no stop or full
  finalization already in flight and no known prior failure, it returns HTTP 500
  (`error:"stop_failed"`). A canceled joiner receives HTTP 503 `stop_pending`
  while finalization remains pending; a known completed failure returns its
  stored error.
- Any actual native cleanup failure, exit callback failure, or required catalog
  persistence failure returning an error without pending finalization returns
  HTTP 500 (`error:"stop_failed"`). Known uncertainty or failure remains an
  error, and SQLite durability is never downgraded.
- HTTP transport disconnect and foreground waiting are separate: if the client
  disconnects during waiting, HTTP delivery cannot be guaranteed, but daemon
  background finalization continues until completed or drained at shutdown. Closing
  a client is separate from Stop.

The legacy terminal socket sends binary output; input may be binary or text,
with text resize JSON `{"type":"resize","cols":110,"rows":40}` reserved.
Opting into subprotocol `console-terminal-v1` separates **binary input bytes**
from **text resize controls**, and sends ordered text
`{"type":"geometry","cols":110,"rows":40}` plus binary replay/live output.
Unsupported offered protocols reject with 400. V1 resize rejects duplicate or
unknown keys, trailing data, nonintegers and dimensions outside 1–65535 with
1008; nonowner input/resize is ignored. Geometry is authoritative only after
successful PTY resize. The socket read limit is 64 KiB; bounded input/output
overflow closes visibly with 1013. Reconnect creates a fresh observation with
bounded replay; there is no durable terminal byte cursor or input replay.
The TUI renderer has narrower qualified geometry bounds; see
[terminal contract](tui-terminal.md) before interpreting backend geometry support
as a renderer or native-platform qualification.

## Watch, limits and events

`GET /v0/watch?workspace={id}&delivery={id}` is a legacy
workspace projection. The workspace selector is a catalog ID, not a filesystem
root; an empty delivery does not adopt the latest journal. Projection I/O
unavailability returns 503 `binding_io`, cancellation returns 408 `binding_canceled`,
and other projection errors return 400 `watch`. Mission clients use the mission
watch endpoint and validate `scope`, `mission_id`, `workspace_id`, `delivery`,
`source` and current evidence. An unassigned mission or missing/changed artifact
can produce a 200 unavailable projection; do not treat it as empty success.

`GET /v0/limits` returns observations with source/origin/measurement and attempt
times. Missing percentage stays null; same-account-set collection is cached for
15 seconds. See [imported quota observations](domain.md#imported-quota-observations).
`GET /v0/activity` returns the latest 100 catalog events.

`GET /v0/events?cursor={sequence}` is a WebSocket stream. Without cursor, the
initial `{type:"snapshot",events:[...]}` contains up to 200 recent events.
With cursor, it contains up to 200 events **strictly after** that nonnegative
int64 decimal sequence. Zero is valid; duplicate, empty, negative, malformed
or overflowing cursors reject with HTTP 400. Live `{type:"event",event:{...}}`
frames follow in batches of 50, with polling around 250 ms after catch-up.
The cursor advances only after a successful frame write. An ahead-of-catalog
cursor stays ahead; it is never silently reset. Clients reconnect from their
last accepted sequence and perform authoritative reads. Catalog events are
refresh signals, not a complete journal stream or mutation receipt. Read/encode
failures close with 1011; client messages on this read-only socket close with
1008. Client cancellation/disconnect closes the read operation, not native work.

## Errors and uncertain effects

Ordinary failures use `{error:"machine_code",message:"human"}`; readiness
errors may add `account_id`. Typed approval/execution/answer failures use their
result envelopes above. Preserve those effects even when HTTP is non-2xx.

| Family | Important statuses / codes |
|---|---|
| Readiness | 409 for account/CLI/authentication guards; 503 for provider unavailable/invalid/forbidden, cancelled or failed/unavailable probes |
| Binding | 400 `binding_request`/`binding_reference`; 408 `binding_canceled`; 404 `binding_missing`; 409 `binding_conflict`; 422 `binding_artifact`; 503 `binding_io`; 500 `binding_store` |
| Plan | 400 `plan_request`/`plan_reference`; 408 `plan_canceled`; 409 `plan_stale`/`plan_binding`/`plan_transition`; 404 `plan_missing`; 422 `plan_artifact`; 503 `plan_io`/`plan_uncertain`; 500 `plan_store` |
| Execution | 409 `execution_approval_required`/`execution_exists`/`execution_owned`; 503 `execution_unavailable`/`execution_closing`/`execution_uncertain`; 408 `execution_canceled`; strict request/reference 400; other plan errors as above |
| Answer | 400 `answer_request`/`answer_reference`/`answer_target`; 409 `answer_stale`/`answer_owned`; 408 `answer_canceled`; 404 `answer_missing`; 422 `answer_artifact`; 503 `answer_io`/`answer_uncertain`; 500 `answer_store`/`answer_recorded_error` |
| Native execution Stop | 400 `execution_reference` (wire validation) / `plan_reference` (service reference); 409 `plan_binding`; 503 `execution_stop_uncertain` / `execution_unavailable`; 408 `execution_canceled`; receipt/canonical validation uses common plan errors; middleware 401 `unauthorized` uses the generic body |
| Session Stop | 401 `unauthorized`; 404 `not_found`; 409 `session_not_owned`; 503 `stop_pending`; 500 `stop_failed` |

The Go client separates `not_dispatched`, `read_only`, `rejected`, `succeeded`
and `uncertain` operation effects and refuses redirects. An accepted HTTP code
with malformed or foreign identity is not authoritative success. Transport
loss/cancellation after mutation dispatch may leave an effect; preserve the
submitted target, confirmed results and draft, quarantine replay and inspect
explicitly. A retry label, acknowledgment or list refresh alone cannot establish
that the original operation had no effect. Product labels are translated;
machine codes, raw diagnostic/provider output and receipt bytes retain identity.
