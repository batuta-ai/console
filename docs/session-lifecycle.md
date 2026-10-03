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
invented, and the saved PID is never signaled. Current owned-session stop
continues to use the Manager; process-tree termination remains a separate task.

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
