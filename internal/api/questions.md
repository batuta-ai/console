# Exact mission questions

`GET /v0/watch?mission=<id>&workspace=<id>` (or the mission watch route)
includes `pending_question` only when the bound native delivery exposes a
waiting question. Existing watch display fields remain available.

```json
{
  "pending_question": {
    "target": {
      "mission_id": "mission-1",
      "workspace_id": "workspace-1",
      "delivery": "delivery-1",
      "task": "task_1",
      "execution": 2,
      "question_id": "sha256:<64 lowercase hex digits>",
      "opened_digest": "sha256:<64 lowercase hex digits>"
    },
    "prompt": "The native question prompt"
  }
}
```

Send every target field, plus `text`, to authenticated `POST /v0/watch/answer`.
`workspace_id` is a catalog ID. The optional legacy `workspace` field, if
present, must equal it; raw roots cannot authorize answers. Missing identity
fails closed. Repeated question digests require the exact execution as well.

The response contains `target`, `effect`, an optional recorded `delivery`, and
an optional `error` object with `code` and `message`:

- `recorded`: Core returned a delivery ID, proving the native answer append.
  HTTP 200 means the catalog event also succeeded. HTTP 500 with
  `answer_recorded_error` preserves that recorded effect when native cleanup
  or the catalog event failed. Do not resubmit the answer.
- `rejected`: No answer was appended by this request. Invalid requests and
  references are 400; stale questions and native ownership conflicts are 409;
  missing references/artifacts are 404; invalid artifacts are 422; cancellation
  is 408. Artifact I/O is 503 and catalog faults are 500.
- `uncertain`: An unclassified native writer error is 503 with
  `answer_uncertain`. Refresh the exact mission/journal before deciding what
  to do. An error alone is not evidence that nothing was written.

`client.AnswerQuestion(ctx, target.Answer(text))` returns `domain.AnswerResult`
and, on failure, `*client.AnswerError` retaining the result, HTTP status and
underlying transport cause. Dispatch-time transport failures, malformed or
mismatched responses, and cancellation after dispatch are uncertain. A context
already canceled before dispatch is rejected. This operation never retries or
follows redirects. Generic `Do`, `Get` and `Post` are unchanged.

The service validates the persisted mission binding and current canonical
workspace, exact plan, delivery and opening before invoking published Core
`loop.AnswerQuestion`. Core rechecks the latest question under native ownership;
Console supplies no engine, journal writer or ownership lock. The
`watch.answered` catalog event records all seven identity fields. Its failure
cannot roll back the native answer.

A native cleanup failure returning a nonempty delivery ID and an uncertain
append failure are preserved by the contract, but are not forced by test hooks.
The tests exercise a real SQLite event rejection and real native validation
failure; neither claims to reproduce those two native fault modes.
