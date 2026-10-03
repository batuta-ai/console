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
// callbacks must not call Stop on the same process or Shutdown on their Manager.
// Stop sends
// immediate SIGKILL to the original PTY process group, including children that
// ignore HUP. Natural leader exit also sweeps that group. Descendants that move
// to another group/session are outside containment.
//
// Successful PTY startup establishes PID = PGID. The exclusively owned unreaped
// leader anchors that number: group signals finish under an authority lock before
// it is irrevocably retired, and only then does the sole Cmd.Wait reap the leader.
// Linux observes exit with Waitid(WNOWAIT); Darwin observes native SZOMB without
// consuming wait status. A map entry, saved PID, birth identity, or membership
// observation never grants signaling authority. Darwin zombie-only group EPERM
// is accepted only after a complete native observation finds no nonterminal or
// unverifiable record in the still-anchored group. Other native errors propagate.
// EndedAt follows the actual leader wait, never a requested signal. Group cleanup
// failures remain errors even when the leader's terminal snapshot is retained.
//
// Shutdown permanently closes admission, includes admitted unpublished starts,
// and joins callbacks, readers and descriptor cleanup. Its context bounds the
// caller's wait, not the continuing cleanup. On timeout, keep callback resources
// alive; a later Shutdown may wait again. Callbacks/subscribers must return for
// draining to complete. Disconnect/unsubscribe does not stop a process; resize
// and input retain their owner-client checks and existing transport semantics.
// Restart recovery lives in service. No catalog PID is adopted or signaled.
package session
