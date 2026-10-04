# Session lifecycle and daemon restart

A session's `process` remains `running`, `exited`, or `unknown`. These describe
Console's lifecycle knowledge, not a promise that a saved PID can be controlled.
The current daemon's Manager owns only the processes and PTYs it launched.
The UI's `owner` and `owner_client` fields authorize client input to that live
transport; they do not establish operating-system process ownership.

## Observed incarnation

New launches capture `process_identity` through native, read-only observation
before their pending snapshot is published. It is opaque metadata: clients must
not parse it or treat it as a control capability. macOS uses the kernel's process
birth timestamp and PID. Linux uses the kernel boot UUID, process start ticks
since boot, and PID, so equal PID/ticks after a different boot cannot match.
Application `CreatedAt`, command names, argv, and saved PID alone prove nothing.
No shell `ps`, new dependency, signal, or privilege change is used for inspection.
On Linux an absent proc record is accepted only with an observable proc mount
without `hidepid` restrictions; hidden PIDs or an unavailable proc filesystem
are unverified. This deliberately avoids interpreting restricted visibility as
physical absence.

A fast exit may leave no captured identity. Capture errors are logged and leave
identity empty; they do not weaken pending admission or turn an already observed
exit back into running. Initial persistence and exit observation share the
Manager's lifecycle lock. Identity accompanies the complete terminal snapshot.

## Recovery before serving

After migrations and service/Manager construction, the daemon reconciles the
catalog before opening an HTTP listener. It never adopts a former process or
PTY. Each unowned `running` row becomes `unknown`, with `owner: none`, an empty
`owner_client`, and one `recovery_reason`:

| Reason | Observation at recovery |
| --- | --- |
| `process_missing` | The OS reports no process, or a zombie/dead record; this also applies to legacy rows without identity. |
| `process_survived` | The live process has the captured incarnation. This proves observation, not authority to signal it. |
| `identity_mismatch` | The saved identity differs from the live PID's observed incarnation. That process is unrelated to the saved lifecycle. |
| `identity_unverified` | Legacy identity is missing, PID is invalid, or inspection is denied, unsupported, malformed, or otherwise unavailable. |

Legacy `unknown` rows with an empty `recovery_reason` (from earlier GET behavior)
receive the same observation and control cleanup once, including an atomic
recovery event. Their existing `EndedAt` and other history remain intact.
An `unknown` row with a nonempty reason is already recovered and stays unchanged.

PID, identity, launch arguments, references, timestamps, and history remain intact.
Recovery never fabricates `EndedAt` or a physical exit, including for missing
processes: there was no owned wait observation with a known termination time.
Existing `exited` rows remain exactly terminal. Sessions still owned by the
current Manager retain their live lifecycle. Already recovered rows remain a
historical observation and are not refreshed on every GET.

Each recovered row and its `session.reconciled` event commit in one transaction.
The event includes session `id`, `recovery_reason`, and `inspection_error` when
inspection failed. A database read, row write, or event failure propagates and
prevents startup from serving. Earlier rows may have committed before a later
failure; retry processes the remaining rows without duplicating events for the
committed ones. There is no automatic retry loop or new busy timeout. Session
GET uses the same serialized recovery operation, reporting persistence errors
instead of silently returning a recovered state that was never committed.
This assumes one daemon Service and its shared Store, not multiple catalog writers.

## Stop, restart, and capacity

A catalog-only stop of an already `exited` row succeeds without changing its row
or events. Other catalog-only stops return recoverable `ErrSessionNotOwned`
(HTTP 409, `session_not_owned`). A running or legacy unrecovered `unknown` row is
first truthfully reconciled; an already recovered row stays intact. Recovery
persistence failures return HTTP 500, `stop_failed`; only a missing session
returns HTTP 404, `not_found`. No `session.stopped` event or physical exit is
invented, and the saved PID is never signaled. Current owned-session stop uses
the Manager's original PTY process group, as described below.

Lost transport and physical liveness are different. Surviving or unverified
former processes **cannot be reattached or controlled by this daemon** and may
still use provider resources. Starting a new session creates a new lifecycle;
it does not stop the old process. Future UI recovery flows must display this
distinction rather than presenting `unknown` as exited or offering old controls.

Capacity belongs to current runtime pending/live admission, not catalog rows or
all processes on the machine. Former `unknown` sessions reserve no slots in a
fresh daemon. A process may exit just after an observation; recovery is evidence
at one instant, not continuous monitoring. Unsupported operating systems report
unverified inspection. Linux runtime qualification remains task40; a cross-build
and parser tests alone do not qualify its runtime or provider behavior.

## Owned groups and clean shutdown

Successful PTY startup creates a new session and process group with the leader's
PID as the original PGID. Authority belongs only to this live daemon's child.
The exclusively owned **unreaped leader** anchors the group number. Every group
signal completes under the same lock before authority is permanently retired;
`Cmd.Wait` then runs exactly once. The leader may already be a zombie, but its
number cannot be reused before collection. A live map entry, matching birth
identity, earlier group membership, or a saved PID cannot replace this anchor.

Stop immediately sends `SIGKILL` to the original group; there is no HUP/TERM grace
period. This includes an original-group child that ignores HUP. Natural leader
exit also triggers a final group sweep before reap. Concurrent/repeated stops
cannot signal after retirement. A failure after spawn, such as log-open failure,
uses the same signal/retire/wait order and preserves both the original error and
cleanup errors. Descendants that change process group or session are outside
this containment: Console does **not** promise to terminate arbitrary descendants.

Linux observes the child with `waitid(WNOWAIT)`; macOS observes native `SZOMB`
without consuming its wait status. Darwin's group kill can return `EPERM` for a
zombie-only group because XNU excludes zombies from eligible signal recipients.
Only while the original authority remains active, a successful complete native
`kern.proc.pgrp` observation of an empty or all-zombie group classifies this
specific error as no remaining group work. If every remaining record is a zombie
or advertises Darwin's `P_WEXIT` flag, the error may instead mark an exit still
in progress. That pending observation does not prove termination: Stop must await
actual leader exit and the final group sweep within its deadline. Only successful
final cleanup resolves this provisional error. Other live/unknown records,
inspection failures, final cleanup failures and timeouts remain errors. This exception is
Darwin-only. Observation never changes the target or authorizes another signal.
Other observation/signaling/cleanup errors are returned or logged, not silently
reported as successful group termination.

The terminal snapshot and physical `EndedAt` follow the leader's actual wait,
not the kill request. Stop waits up to two seconds for callback and descriptor
cleanup, returning an error if completion is still pending. The latest terminal
snapshot and released runtime slot survive catalog persistence failure; the
service logs its write error. Leader exit does not by itself prove successful
group cleanup, so a terminal snapshot may accompany a cleanup error.

On SIGINT, SIGTERM, or a serving failure, the daemon permanently closes Manager
admission. Starts already admitted but not yet published are included in drain;
when they publish, they are stopped through their original owned authority.
The daemon waits for leader collection, exit callbacks, PTY readers and file
closure before closing the catalog. It also bounds HTTP shutdown within the
same five-second budget. Cleanup failures or expiry produce a nonzero daemon
exit. On timeout, callbacks/handlers may still be active: the catalog is left
open until process exit rather than being closed underneath them. A timeout is
not a claim of complete cleanup. Manager callers can wait again with a new
context, and must keep callback resources alive until draining completes.
Callbacks and subscriber functions must return for a complete drain. Every
cleanup error is logged, and Stop reports its session's failure. Manager retains
the first unresolved group/wait failure and the first current drain failure,
without accumulating an unlimited error history. A descriptor failure from a
completed earlier session remains reported to its Stop caller but does not make
a later empty drain fail.

Recovered catalog-only rows and unrelated OS processes are never part of this
drain. Clean shutdown persists current owned exits before the next startup
reconciles historical rows. Client detach/unsubscribe leaves the same owned PID
running; reconnect resumes that transport. Input and native PTY resize continue
to require the current owner client. Resize changes terminal dimensions without
changing group authority or stopping the process.

## Internal diagnostics

On macOS and Linux, Manager also runs internal diagnostic commands without a
PTY, session row, replay buffer, transcript or exit callback. Commands receive
the same closed runtime environment as sessions. Without an explicitly supplied
private HOME, each command gets a temporary credentialless HOME that is removed
after its owned group, command waiter and output readers finish.

Diagnostics have a five-second maximum deadline and a shared 32 KiB budget for
stdout and stderr. Only a successful command with completed cleanup returns
stdout in memory. Stderr is discarded; errors, cancellation, excess output and
unsuccessful exits return no raw output. Callers must not persist returned
credential-bearing data in session logs. Unsupported platforms refuse the
command before spawning.

A successful Setsid start establishes the same unreaped-child group authority
as a PTY session. Natural exit, cancellation and Manager shutdown sweep only
that original group before the sole command waiter reaps the leader. Shutdown
closes diagnostic admission and waits for admitted commands, including starts
racing shutdown. A caller timeout may return before cleanup finishes; Manager
retains that work until drain completes. Escaped groups are outside containment.
After leader exit, a 250 ms pipe-drain bound closes inherited output descriptors
that escaped descendants keep open and reports an error instead of successful
truncated output. Ordinary nonzero diagnostic exits and output-limit errors do
not count as failed group cleanup.

## Storage and sources

Schema 3 adds non-null `process_identity` and `recovery_reason`, defaulting to
empty strings. Schema 2 upgrades preserve all existing fields and events.
Migration failure rolls back that migration's DDL and version; future schemas
are refused unchanged. Terminal-row protection covers both added fields.

The implementation follows the pinned `golang.org/x/sys` Darwin
`SysctlKinfoProcSlice` and `ExternProc.P_starttime` definitions. The slice API can
represent an empty kernel result without the singular API's `EIO` ambiguity.
Kernel behavior is documented in [XNU kern_sysctl.c](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sysctl.c)
and [proc.h](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/proc.h).
Linux field meanings come from the [kernel proc documentation](https://www.kernel.org/doc/html/latest/filesystems/proc.html).
Sources checked 2026-10-03; no foreign-platform runtime result is inferred.

Group lifetime and signaling follow POSIX Issue 8
[process ID lifetime (4.17)](https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap04.html),
[kill](https://pubs.opengroup.org/onlinepubs/9799919799/functions/kill.html),
[wait](https://pubs.opengroup.org/onlinepubs/9799919799/functions/wait.html), and
[setpgid](https://pubs.opengroup.org/onlinepubs/9799919799/functions/setpgid.html).
Darwin's zombie exclusion and EPERM behavior are in
[XNU kern_sig.c, killpg1](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c);
the group query in `kern_sysctl.c` covers both live and zombie lists without an
implicit UID filter. No raw Darwin wait syscall ABI is assumed.
