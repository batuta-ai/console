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
// OnExit must be configured before starting processes. Stop separates native
// resource termination from full durable finalization. The native phase
// revokes input and invokes platform-owned process termination; it then waits
// for leader exit observation, final group sweep or job cleanup, and
// authority retirement, with platform-native handle collection or Cmd.Wait
// where an exec child exists, slot release, PTY descriptor closure,
// and joining output readers and terminal subscribers within an owned
// two-second resource waiter. This two-second timer bounds only that following
// resource wait, not the prior platform-owned stop, the entire native phase, or
// the whole Stop method, and does not bound exit callbacks or catalog
// persistence. Exceeding the two-second resource wait returns a native
// incomplete error reflecting uncertainty, which is not retained as definitive
// if clean completion occurs later.
//
// Full durable finalization additionally executes OnExit (persisting
// session.exited), closes the transcript log, and removes private HOME
// directories. StopContext bounds foreground waiting only after the native-stop
// phase has returned; context cancellation while finalization remains pending
// returns FinalizationPendingError with the SessionID and cancellation Cause
// alongside the latest truthful snapshot and any joined cleanup errors, never
// durable success. Completed finalization takes precedence over simultaneous
// cancellation. StopAndFinalize tracks and joins concurrent stop callers and
// required catalog finalization, keeping work joinable through Shutdown. Exit
// callbacks must not call Stop on the same process or Shutdown on their Manager.
// On Unix, stop sends immediate SIGKILL to the original PTY process group,
// including children that ignore HUP, and natural leader exit also sweeps that
// group. Descendants that move to another group/session are outside containment.
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
