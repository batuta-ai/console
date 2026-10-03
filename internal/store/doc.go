// Package store persists the Console catalog and its events in SQLite.
//
// Schema versions are stored in PRAGMA user_version. Version 0 is an
// unversioned database; version 1 creates or adopts the six catalog tables;
// version 2 adopts squads.origin and missions.brief, adding only missing
// columns with defaults "custom" and "" respectively. The known legacy
// layouts have neither column, origin only, or both columns. Adoption keeps
// existing values, JSON bytes, timestamps, references, events and sequence
// numbers. It does not repair references or bind legacy deliveries.
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
