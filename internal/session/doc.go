// Package session owns processes launched by one live daemon's Manager.
//
// StartPending atomically reserves an account slot before spawning. Pending
// starts, including those awaiting catalog persistence, and live processes use
// the same count. Positive capacities are bounds; nonpositive capacities are
// unlimited, and sessions without an account are unbound. Spawn failure releases
// the reservation after cleanup. The wait goroutine releases it on observed
// termination, before OnExit, even when that callback's catalog write fails.
// Catalog rows are not reservations and are never adopted as owned processes.
//
// A Pending handle retains the process's latest snapshot through initial
// persistence without retaining a slot after termination. Persist and exit
// observation share the process state lock: an already observed exit, including
// EndedAt, is written and returned instead of an initial running snapshot. An
// exit following a legitimate running response remains possible. The persistence
// callback must not reenter Manager operations on the same session. Callers
// stop newly spawned processes if initial persistence fails, preserving the
// persistence error and any cleanup failure.
//
// A spawn captures process_identity before publishing the pending handle. Native
// inspection uses Darwin's kernel birth timestamp, or Linux's boot UUID plus
// start ticks and PID. Neither argv nor application CreatedAt is identity.
// Inspection errors leave identity empty and are logged; a process that exits
// before capture need not have identity. InspectProcess observes only: missing
// includes zombies/dead records, and errors mean unverified, never absent.
// The observation can become stale immediately and grants no control authority.
//
// OnExit must be configured before starting processes. Stop waits for termination
// and callback completion, reporting kill errors or a completion timeout. Exit
// callbacks must not call Stop on the same process. Stop kills only the process
// leader; process-tree termination is a separate concern. Restart recovery lives
// in service. No process is adopted or killed by catalog PID.
package session
