// Package service coordinates the catalog with one daemon's shared Manager.
//
// All account-bound start paths pass the account's current SessionCap to the
// Manager's atomic admission owner. Runtime pending/live counts, not catalog
// rows, determine capacity. Legacy init sessions have no account and remain
// unbound. Login and init serialize their reuse check through initial persistence
// so concurrent calls reuse one live process without reserving an extra slot.
//
// Initial entity/event writes use the Manager's latest lifecycle snapshot.
// Failed persistence stops the new process and returns the original failure,
// joined with any cleanup failure. Natural exit releases runtime admission
// independently of the catalog callback; asynchronous persistence failures are
// logged. Before opening its listener, the daemon reconciles every unowned
// running row, and legacy unknown rows with an empty recovery_reason, to unknown
// with read-only OS evidence and an atomic
// session.reconciled event. Reasons are process_missing, process_survived,
// identity_mismatch, or identity_unverified. Recovery clears obsolete client
// control, preserves launch/history metadata, and never fabricates EndedAt.
// Failure prevents serving; committed rows are not
// reconciled again on retry. Current Manager sessions and terminal rows retain
// their lifecycle. GET uses the same serialized, idempotent recovery path.
//
// Process identity proves an observed incarnation, never authority to signal or
// adopt it. Former PTYs cannot be reattached or controlled. Catalog-only stop
// of an exited row succeeds without changing history; other catalog-only stops
// return ErrSessionNotOwned without inventing exit. Surviving or unverified
// former processes may still consume provider resources, but never reserve
// slots in this daemon. Future recovery UI must show that distinction.
// Process-tree termination remains outside this restart policy.
package service
