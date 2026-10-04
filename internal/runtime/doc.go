// Package runtime owns Console's local data paths, auth token and daemon lock.
//
// The runtime home must be a real directory owned by the effective user without
// group/other write permission. Resolve validates it before creating logs and
// never changes an existing home's mode. Normal ancestor aliases are trusted.
//
// All LoadOrCreateAuth callers coordinate through the persistent auth.secret.lock
// file before reading or creating auth.secret. Both files must be regular,
// single-linked and owned by the effective user. Permissions are corrected to
// 0600 through the validated descriptor, never a followed pathname. Links,
// special files and failed validation or I/O return errors without a token.
//
// Existing token bytes of length at least 32 are preserved, including their
// inode; new tokens are 64 hexadecimal characters, written and synced in a private
// staging file before non-replacing publication under the initialization lock.
// Known write failures remove that staging file, with cleanup errors reported.
// Empty/short pre-existing contents are refused and are never silently rotated.
// Abrupt termination can leave private staging files, which are never adopted.
// Termination between publication and staging unlink can leave a linked final
// file; validation refuses it. Explicit recovery requires the daemon stopped.
// Coordination files are not deleted
// while cooperating callers might use them. Daemon lock metadata is written only
// after exclusive ownership; ReadLock metadata alone does not prove readiness.
//
// Darwin shared-leaf creation retries ENOENT up to three native calls with the
// same validated parent and security flags, after a reproduced first-create
// failure. Persistent errors remain errors. The lower-level cause is unconfirmed;
// this is a marked platform workaround, not a general error retry policy.
//
// These guarantees target cooperating macOS/Linux processes on local filesystems.
// They do not provide a sandbox against hostile same-user namespace mutations,
// validate filesystem ACLs, or establish network-filesystem/full crash durability.
package runtime
