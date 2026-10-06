# Exact plan detail and immutable approval (v0)

These authenticated endpoints record explicit human approval in Console. They do
not execute a plan, change its native status, open a delivery, or write a Core
journal. Native execution integration belongs to task21 and remains unqualified.

## Read the complete editable plan

`GET /v0/missions/{id}/plan?workspace_id={catalog-id}` requires a Bearer token and
an exact catalog mission/workspace association. No root path, latest-plan lookup,
or delivery discovery is accepted. The workspace query parameter is mandatory;
unknown, duplicate and malformed query parameters are rejected.

The response is `domain.PlanDetail`:

- `identity`: `mission_id`, the complete persisted `binding`, and `version`.
- `content`: base64 encoding of every original byte (at most 1 MiB). Decode these
  bytes for human review. There is no line-ending or UTF-8 normalization.
- `title`, `goal`, `context`, `created`, `inputs`, `status`, and `tasks`: projections
  from the published Core parser applied to the same bytes. Tasks include native
  IDs, numbers, title/status, domain/complexity, dependencies, executor/model,
  scopes, acceptance criteria and source line.
- `approval`, when present: the receipt matching this exact mission, binding and
  content version. A native `approved` header alone never produces a receipt.

`version` is lowercase hexadecimal SHA-256 over the entire decoded `content`.
It is separate from `binding.plan_digest`, Core's task-set digest. Prose, stamps,
context and line-ending edits change the version even when the task set does not
change. Binding contains workspace ID/root/canonical root, plan path/slug/task-set
digest and optional delivery/opening identity. Retain the complete identity.

Missing or unassigned bindings, changed roots/task sets, unsafe paths, symlinks,
nonregular/oversized artifacts, detected truncation or concurrent read changes,
and competing active/legacy files produce errors, not an empty successful view.
Core still decides whether source syntax is valid. Reads cannot infer that an
otherwise valid file was historically shortened before observation.

## Approve exactly what was shown

`POST /v0/missions/{id}/plan/approvals` accepts the complete `identity` object
returned by detail as its JSON body. The mission ID in the body must equal the
route. No query parameters are allowed. JSON is limited to 16 KiB, rejects unknown
fields and trailing values, and requires the shown SHA-256 and full identity.
Send this only after explicit human approval of the decoded full content.

The source is reread through anchored, no-follow, bounded file access and parsed
by Core. Its complete digest must still equal the shown version. SQLite reserves
the write transaction before rechecking the current catalog workspace and binding,
and before reading the source. It atomically inserts an immutable snapshot/receipt
and one `plan.approved` event. Exact duplicate approvals return the existing receipt
without adding an event; they still validate current identity and source first.
A changed binding cannot pass using an earlier service preflight.

The source-validation point is completion of the bounded native read, metadata
checks, native parse, binding checks and digest comparison inside that transaction.
This is not a lock on external writers. A writer can change or replace the file
between validation and database commit. Only the validated bytes are stored; later
edits are distinct unapproved versions. Nothing renames over the editable source,
rewrites its header, changes its permissions, or claims to exclude arbitrary writers.
A receipt therefore describes exact historical bytes, not a guarantee that the
current source still matches them.

Only `proposed` and `approved` active/legacy sources without a bound delivery are
eligible. `in progress`, `done`, archived plans and missions with started/bound
deliveries cannot gain an initial-execution approval here. Task-set changes require
explicit rebind under task18 rules. Existing delivery ownership stays immutable.
Prose-only edits can receive a new explicit receipt with the old snapshot retained.
Different missions sharing the same source have independent receipts.

Successful response:

```json
{
  "target": {"mission_id":"…","binding":{},"version":"…"},
  "effect": "recorded",
  "receipt": {
    "id":"approval_…",
    "identity":{"mission_id":"…","binding":{},"version":"…"},
    "content":"<base64 exact bytes>",
    "approved_at":"<RFC3339 timestamp>"
  }
}
```

The abbreviated bindings above represent the full object, not a valid request.
The receipt and its source bytes are one immutable SQLite row, protected against
update/delete through normal SQL. Migration 5 creates no legacy approvals. No
native status is inferred or altered: a `proposed` header remains `proposed` in
its approved snapshot. A receipt attests authenticated Console approval, not the
identity of a particular human (the existing shared-token auth policy is unchanged).

## Retrieve a historical snapshot

`GET /v0/missions/{id}/plan/approvals/{receipt}?workspace_id={catalog-id}` returns
the persisted receipt including original bytes after restart, rebind, edit or source
removal. It requires matching catalog mission/workspace and receipt ownership; it
does not require today's source to match. There is no global receipt lookup.

Task21 can consume these exact snapshot bytes and immutable binding/version, but
must independently validate execution eligibility. Task20 uses only Core's public
`routing.ParsePlan` and `routing.PlanLocations` interfaces. The linked Core
`v1.1.0-beta.49` exports `loop.ApprovedPlanSnapshot` with `Slug`, `Path`,
`ReceiptID`, `ContentDigest` and `Content`, accepted through
`loop.Options.ApprovedSnapshot`. Core owns a copy of the bytes and validates the
canonical plan path, receipt and lowercase SHA-256 content digest. The snapshot
path accepts proposed or approved plan headers without rewriting the source;
the caller owns authenticated authorization and durable receipt storage.

`loop.New` prepares a fresh delivery but does not journal it until `Run`. A
caller-selected `DeliveryID` must be valid and cannot reopen an existing
delivery. `loop.Resume` checks the original workspace, slug, path, receipt,
content digest and task-set identity before acquiring delivery ownership;
missing or differing approvals return `ErrSnapshotRequired` or
`ErrSnapshotMismatch`, while invalid snapshots return `ErrSnapshotInvalid`.
Successful Resume ownership must be released, including after a dry run.
Console does not yet use native New/Resume. Task21 must integrate this contract;
this receipt alone cannot start execution, and no provider/runtime qualification
is implied by adopting the public module.

## Errors and client behavior

Detail/receipt failures use `{"error":"code","message":"…"}`. Approval failures
use `target`, `effect`, and `error: {code,message}`. Authentication failures retain
the existing API error shape. Codes and HTTP statuses:

| Code | Status | Meaning |
| --- | --- | --- |
| `plan_request`, `plan_reference` | 400 | Invalid body or incomplete/foreign reference |
| `unauthorized` | 401 | Missing/invalid Bearer token |
| `plan_missing` | 404 | Missing catalog object, source or scoped receipt |
| `plan_stale` | 409 | Full source digest differs from the shown version |
| `plan_binding` | 409 | Binding/root/task-set changed or unassigned |
| `plan_transition` | 409 | Not eligible for initial-execution approval |
| `plan_canceled` | 408 | Request canceled before a known commit |
| `plan_artifact` | 422 | Unsafe, competing, invalid or unstable source |
| `plan_io` | 503 | Source observation failed |
| `plan_store` | 500 | Catalog/storage/event operation failed |
| `plan_uncertain` | 503 | Commit returned an error; reconcile persisted evidence |

The typed context-aware Go client provides `PlanDetail`, `ApprovePlan`, and
`PlanApproval`, with `*client.PlanError` preserving status, code, cause and approval
effect. Client responses are bounded to 16 MiB (including base64, projections and
optional receipt) and validated for identity, content digest and receipt integrity.
Cancellation before dispatch is rejected. Transport loss, malformed/mismatched
responses or redirects after dispatch yield `uncertain`, not an asserted rejection.
The client does not redirect or automatically resend an approval.

After an uncertain result, refresh exact mission detail before any explicit retry.
If the editable file changed or disappeared, a previously known receipt ID can be
retrieved directly; otherwise an operator can reconcile the `plan.approved` event
and durable receipt. Absence of approval on a newer detail is not evidence that an
older version never committed. `recorded` always includes durable receipt evidence;
`rejected` means no approval was made by that request; `uncertain` makes no such claim.
