// Package runtime owns Console's local data paths, auth token and daemon lock.
//
// On macOS/Linux, the runtime home must be a real directory owned by the
// effective user without group/other write permission. Resolve validates it
// before creating logs and never changes an existing home's mode. Normal
// ancestor aliases are trusted on these platforms.
//
// Windows requires an absolute path on a fixed local NTFS volume with persistent
// ACL support. Components are opened relative to held directory handles without
// following reparse points. Existing ancestors must have trusted owners and no
// effective foreign grant to delete children, change attributes, or change
// ownership or permissions. Private homes and files require the current user SID
// as owner and a protected DACL granting full access only to that SID. Unsafe
// existing objects are refused without repairing their ACLs or replacing them.
//
// All LoadOrCreateAuth callers coordinate through the persistent auth.secret.lock
// file before reading or creating auth.secret. Both files must be regular,
// single-linked and owned by the effective user. Unix permissions are corrected
// to 0600 through the validated descriptor, never a followed pathname. Windows
// files retain their verified private ACLs. Links, special files and failed
// validation or I/O return errors without a token. Windows coordination waits
// have a deadline and cancel and drain pending native lock requests on timeout.
//
// Existing token bytes of length at least 32 are preserved, including their
// inode; new tokens are 64 hexadecimal characters, written and synced in a private
// staging file before non-replacing publication under the initialization lock.
// Known write failures remove that staging file, with cleanup errors reported.
// Empty/short pre-existing contents are refused and are never silently rotated.
// Abrupt termination can leave private staging files, which are never adopted.
// On Unix, termination between publication and staging unlink can leave a linked
// final file; validation refuses it. Windows uses a descriptor-relative rename
// that never replaces an existing final file. Explicit recovery requires the
// daemon stopped.
// Coordination files are not deleted
// while cooperating callers might use them. Daemon lock metadata is written only
// after exclusive ownership. Windows metadata readers share a separate native
// byte-range lock with publishers and enforce a size limit. ReadLock metadata
// alone does not prove readiness or establish process control authority.
//
// Darwin shared-leaf creation retries ENOENT up to three native calls with the
// same validated parent and security flags, after a reproduced first-create
// failure. Persistent errors remain errors. The lower-level cause is unconfirmed;
// this is a marked platform workaround, not a general error retry policy.
//
// These guarantees target cooperating processes on local filesystems.
// They do not provide a sandbox against hostile same-user namespace mutations,
// privileged administrators, or establish network-filesystem/full crash durability.
// Unix mode checks do not validate filesystem ACLs.
package runtime
