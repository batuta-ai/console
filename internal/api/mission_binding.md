Mission binding (API v0)
========================

`PUT /v0/missions/{id}/binding` requires the existing bearer token and a JSON
object with `workspace_id`, `plan_path`, and optional `delivery_id`. For example:

```json
{"workspace_id":"ws_example","plan_path":".batuta/plans/example.md","delivery_id":"example-run"}
```

The response is the mission, including its persisted `binding`. Omitting
`delivery_id` associates only the plan. Before a delivery is bound, an explicit
request can replace the plan association. Once a delivery is bound, only an
exact resubmission is accepted; it returns the existing association without a
new event, even if artifacts have since disappeared. It cannot clear, replace,
or steal a delivery. Delivery ownership is unique within both the catalog
workspace and its canonical filesystem root. Identical IDs in different roots
are independent.

The selected path must be an exact active `.batuta/plans/<slug>.md`, legacy
`.batuta/plan-<slug>.md`, or archived `.batuta/plans/done/<slug>.md` path. Active
and legacy alternatives for the same slug are rejected as ambiguous. Core's
`routing.ParsePlan` validates the plan and supplies `TaskSet.Digest`; Console
does not calculate a substitute plan digest. That digest describes core's task
identity, not all prose or status metadata in the file.

When a delivery is supplied, Console reads that journal through an anchored,
no-follow descriptor and core's `journal.Decode` hash-chain verifier. The
single initial `loop.KindOpened` record must match the slug, plan digest,
workspace's canonical root, and plan path. An explicitly selected archive may
match the corresponding original active or legacy path. The opening record's
digest is also persisted to detect replacement of the journal incarnation.
Core bookkeeping that changes the task digest makes the artifact unavailable;
this API does not implement plan-version transitions.

`GET /v0/missions/{id}/watch` returns only that mission's bound plan and journal
projection. `GET /v0/watch?mission={id}` is an equivalent explicit selector.
Optional `workspace` and `delivery` query parameters must match the mission.
The response includes `scope: "mission"`, `mission_id`, `workspace_id`, and
`source`. An unassigned mission has `available: false`,
`source: "unavailable"`, and `message: "mission_unassigned"`. A plan-only binding
shows its single plan with `message: "delivery_unassigned"`. Missing, moved,
changed, ambiguous, or replaced artifacts remain unavailable with a diagnostic
message. Watch never moves the saved path to an archive or selects a replacement.

Legacy `GET /v0/watch?workspace={id}` remains workspace plan discovery with
`scope: "workspace"`; without an explicit delivery it never selects a journal.
Mission watch does not use workspace-wide presence counts or filesystem log
reads. Progress and evidence come solely from the verified bound journal through
core's `loop.PanelModel`.

Binding errors are 400 for invalid references/request syntax, 404 for missing
catalog references/artifacts, 409 for immutable or conflicting ownership,
422 for invalid/ambiguous artifact identity or corrupt records, 503 for artifact
I/O failures, and 500 for catalog failures. Errors expose no artifact contents
or raw storage errors. Missing/changed artifacts during watch are represented
by an unavailable view; I/O failures remain HTTP errors.

Schema 4 leaves historical missions unassigned. Binding columns and a
`mission.bound` event commit atomically; the event includes mission/workspace
IDs, catalog and canonical roots, exact path, slug, plan digest, delivery ID,
opening digest, and previous binding. Reads do not create directories or events,
write journals, start processes, invoke models, answer questions, or approve work.
