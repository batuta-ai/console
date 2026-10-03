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
// logged. Restart reconciliation and process-tree termination are not provided
// by this live-daemon admission policy.
package service
