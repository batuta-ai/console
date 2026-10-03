// Package store persists the Console catalog and its events in SQLite.
//
// Schema versions are stored in PRAGMA user_version. Version 0 is an
// unversioned database; version 1 creates or adopts the six catalog tables;
// version 2 adopts squads.origin and missions.brief, adding only missing
// columns with defaults "custom" and "" respectively. The known legacy
// layouts have neither column, origin only, or both columns. Adoption keeps
// existing values, JSON bytes, timestamps, references, events and sequence
// numbers. Version 3 adds sessions.process_identity and recovery_reason as
// non-null text with empty defaults; legacy PID/CreatedAt never synthesize an
// identity. Existing fields and event history remain unchanged. It does not
// repair references or bind legacy deliveries.
//
// Each migration's DDL and version change commit in one transaction. Failure
// identifies the version and operation, rolls back that migration, and leaves
// earlier committed migrations intact. Open fails until the cause is removed;
// reopening then retries the failed migration. A version newer than this
// package supports is refused without changing the database.
//
// Bare Put methods write only the entity and never emit implicit events.
// PutWorkspaceWithEvent, PutSquadWithEvent, PutMissionWithEvent,
// PutAccountWithEvent and PutSessionWithEvent commit one entity mutation and
// its caller-specified event together, or roll back both on any write,
// serialization or event error. Event kinds and payloads belong to callers.
// AppendEvent reports serialization, SQL and generated-sequence errors.
// Session rows are terminal once process is exited: subsequent puts preserve
// that complete row, including EndedAt. WithEvent still commits the requested
// event in its transaction, even if a late initial snapshot cannot replace the
// terminal row. Session IDs identify one process lifecycle and are not reused.
//
// Each Store uses one pooled SQLite connection. Reads, bare Put calls,
// AppendEvent, migrations and entity/event transactions share that connection,
// serializing database work within the Store. A transaction must use its own
// catalogWriter, never reenter the pool. Read cursors must be exhausted or
// closed before another database operation; ListSessions scans complete rows
// directly rather than holding a cursor across GetSession calls. This avoids
// pool starvation and read locks blocking a concurrent writer's commit.
//
// Long reads therefore delay writes. This policy does not serialize separate
// Store handles or other processes opening the same file: use one shared Store
// per daemon catalog. Cross-handle/process contention can still return SQLite
// locking errors under the unchanged busy timeout; errors are not hidden or
// retried. This is not a multi-process writer coordination mechanism.
//
// These transactions do not make multi-step workflows atomic, and do not
// encompass processes, files, live session ownership or the core journal.
// Service callers stop a newly spawned process if its persistence fails and
// return the persistence error, including any reported cleanup failure.
// Asynchronous exit persistence failures are logged. Answer may already have
// affected the core journal when its catalog event fails; its error explicitly
// acknowledges that effect. Retrying external effects requires caller care.
//
// Catalog events are retained indefinitely. There is no automatic pruning.
// EventsSince and EventsRecent default to bounded reads (200 and 100 events),
// which do not delete or otherwise shorten the retained history.
package store
