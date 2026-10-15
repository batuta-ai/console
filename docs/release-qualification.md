# Windows portability qualification

Task42 prepares source and test portability. Native Windows operation remains
unsupported until Task43 qualifies effective protection, process ownership and
ConPTY. Cross-compilation and WSL2 do not qualify native Windows behavior. Real
Codex/Claude provider qualification belongs to Task44.

## Blocking native Windows source gate

The reusable Console checks workflow now includes a blocking `windows-source`
job on native `windows-2025` amd64. It selects the same exact
`inputs.ref || github.sha` as the unchanged macOS/Linux source job, with pinned
Go 1.27.2, Node 26.10.0 and npm 12.2.0. It runs web tests, TypeScript checking
and the embedded web build, rejecting tracked and untracked embedded asset
drift. Checkout disables automatic CRLF conversion, and the asset guard uses
command-scoped `core.autocrlf=false` and `core.eol=lf`, without global Git changes.
The native Go path downloads the selected modules before verifying their cached
integrity, builds both commands, runs vet and requires
`go test ./... -json -count=1 -timeout=20m` without selections or suppressions.
A nonzero process exit, any JSON failure event or any test-level skip blocks the
job. A package-level skip is allowed only with matching output for that same
package reporting `[no test files]`; unexplained package skips block the job.
Allowed empty packages are recorded separately in the summary diagnostics and
are not skipped tests. No actual test skips are allowed.

Before Go checks, the job records the actual current-user SID and reads the owner
SID and DACL of each existing directory from RUNNER_TEMP and the current user's
resolved UserProfile through their roots, deduplicating shared ancestors. It then
creates a new empty disposable directory with a short name directly under that
UserProfile, matching the prior native harness's fixture location. It verifies
the new directory's protected ACL: current-user SID owner and only that SID's
full control rule with object/container inheritance. Only the test process
environment receives this directory as TEMP, TMP and LOCALAPPDATA. No existing
directory ACL, profile, token, global temporary environment or runner security
setting is changed. The fixture ACL does not change the token's default owner
or make the runner a standard user. A dedicated GOCACHE under runner.temp is
shared by Go builds and tests, so changing LOCALAPPDATA for tests does not
introduce a separate cold build cache. The cache and evidence directories under
runner.temp are ordinary job artifacts; no current-user SID ownership claim is
made for them or their ancestors.

An `always()` upload retains complete Go JSON stdout, stderr and process exit,
plus runner OS/version, identity/privileges, toolchain, source revision, ancestor
owner/DACL records, fixture ACL and stage diagnostics from a separate runner.temp
evidence directory.
No personal credentials or configuration are collected. The upload action is
pinned to v7.0.2 commit `cf430e030ddbb5b0abf93d22962f4752f3646cd9`.
Local lint/source review does not establish runtime success; a reviewed/pushed
candidate still needs a live Windows CI run and inspection of its artifacts.

[GitHub documents Windows hosted runners as administrators with UAC disabled](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).
This elevated source gate cannot close standard-user Windows qualification.
The earlier full native candidate `0747deb` recorded 3,250 passes, 107 failures
and zero skips. The conductor reported public Windows run `38551760992` at
`4612227`: builds, vet and web checks passed, while the complete Go suite recorded
2,334 passes, 987 failures and zero test skips. Raw evidence remains at
`.batuta/runs/2026-10-14-windows-beta1/native-ci-461-push`. Many PrivateHome
constructors failed on a foreign Windows owner in the runtime parent before
reaching their intended contracts. That run placed the fixture under RUNNER_TEMP
on drive D, unlike the prior harness's UserProfile root; the offending ancestor's
SID was not captured. The revised fixture location and ancestor diagnostics still
require a fresh native CI run; no failure is claimed fixed by local source review.
Full race/scanner, secure source/authentication, real provider and standard-user
qualification remain outside this bounded gate. Windows remains pending until
Task43/Task44 gates pass. Windows remains selected by SelectedPlatform, which
represents selection rather than qualification; packages remain macOS/Linux only.
This change neither qualifies a full Windows release nor closes beta1.

## Fixture command and scope — 2026-10-13

```sh
sh scripts/qualify-release.sh --fixtures
```

This is the only supported mode, for native macOS/Linux with Go 1.27.2,
Node 26.10.0, npm 12.2.0, cached pinned Go modules, loopback sockets and PTYs.
Select the pinned tools on PATH (and GOROOT if set) before running. The command
uses an owned temporary build cache, HOME and output directory; `GOMODCACHE`
may point to a prepared module cache. Module downloads are disabled. It prints
exact stage commands, fails on errors/timeouts/skips, and requires every named
test both to exist before selection and to produce a passing test event.
The cache must already contain both Console's pinned build list and the pinned
Core release's own build list, including their checksum records. The API journey
builds the Core CLI from its module directory, while the TUI journey builds it
from Console's module context. Prepare both lists before offline qualification:

```sh
go mod download
core_dir=$(go list -m -f '{{.Dir}}' github.com/batuta-ai/core)
(cd "$core_dir" && go mod download)
```

`GOPROXY=off` keeps resolution offline, while `GOSUMDB=sum.golang.org` and
empty private/no-sum exclusions retain public checksum policy. `go mod verify`
checks cached module integrity in both module contexts before test inventory or
builds. A missing or modified cached module stops qualification.
Missing `scripts/qualify-release.sh` was the baseline missing-capability red,
not a product behavior regression.

Fresh current-source console/consoled builds report public `-version` JSON;
the command compares diagnostic fields with the SDK's `go version -m -json`
output and the current Git revision. It checks Go and unreplaced Core
`v1.1.0-beta.49`, preserving the observed module version (including a Go-derived
pseudo-version) while requiring release labels to remain development/unknown/unset.
The final PASS line records the observed revision and modified state; a dirty
source run qualifies those current bytes, rather than the commit alone.
It then runs `web/tests/daemon-journey.mjs`, which builds its own
current consoled and restarts that same binary against one disposable catalog.
The HTTP journey covers two accounts/vaults, a custom squad revision, two
independent mission snapshots, configuration/init-negative cases and persisted
authenticated rereads. It performs no successful login/init or model execution.
Both normal daemon stops require exit zero without a signal or forced kill;
the original five-second SIGKILL fallback still drains a stuck owned child.

Stage deadlines are 180 seconds by default (including builds, module verification
and test inventory), 10 seconds for cache/revision/build-information/diagnostic
reads, 240 seconds for the HTTP journey, and 22 minutes for each fixture package.
Go test inventory uses `-timeout=120s`; fixture execution uses `-timeout=20m`,
admitting the heap fixture's unchanged 10-minute context plus the other tests
and cleanup. Stage termination escalates after 10 seconds. The HTTP journey's
original build/startup/request/stop deadlines remain 180/10/5/5 seconds.

The required fixture list in the script joins these existing proofs:

- `TestJourneyPublicCoreQuestionResumeAndRestart`: model-driven TUI, pinned
  public Core, synthetic CLIs, exact approval/question/resume, two missions
  and service/store restart. This is not full daemon execution restart.
- `TestJourneyRealDaemonPlanningBindingApprovalTwoMissions`: TUI model plus
  real service/API over in-process transport and local TLS/readiness helpers.
- `TestAccountOfficialLoginOpensActualTerminal` and
  `TestWorkspaceInitActualHTTPWebSocketPTY`: actual terminal attachment/control
  and PTYs, with substituted account/login/init DTO handlers; neither proves
  completed official login or full real Workspace init.
- Explicit `TestTerminalNative…` selections in TUI and API cover raw keyboard
  input, control, ownership, resize, stop and transport fixtures.
- `TestNativeApprovedHTTPJourneyUsesOneDeliveryAfterAnswerAndDeletion`:
  approved HTTP execution with public Core, a synthetic CLI and local auth
  fixture; it does not contact a real provider.

The conductor ran the expanded HTTP journey successfully on candidate
`d3e1143dccdae506685d53fe7a24910f5c0c1b8f`; its qualifier failed on the
incorrect `(devel)` assumption. The retry locally passed pinned toolchains,
cached module integrity, required-test inventory, fresh builds and both revised
provenance checks. It then failed with sandbox `listen EPERM` on `127.0.0.1`
before consoled startup. Source-extracted shutdown checks using owned Node
children passed zero/nonzero/signal/forced-kill cases; these do not establish
native daemon success. The conductor subsequently ran the revised qualifier on
native Darwin/arm64 and Linux/amd64 in a Docker Desktop container as UID 1000:
each passed 40 test events without skips, plus the HTTP catalog/restart journey
and builds. A controlled shutdown mutant was rejected. Independent review then
identified the separate Core build-list verification gap; an owned cache with
a harmless change to Core-only `x/sync v0.21.0` reproduced that gap. Verification
now covers both module contexts. Candidate `21bf25e` passed on native
Darwin/arm64 and Linux/amd64 in the same UID-1000 container, with 40 passing
test events per platform and no failures or skips.

The integrity red/green pair used one complete, owned module-cache copy with
only `golang.org/x/sync@v0.21.0/README.md` altered. The original cache's copy
of that README kept its SHA256. In each source checkout the exact command was:

```sh
GOMODCACHE=/tmp/console-journey-core-cache-canary-complete-21bf \
  sh scripts/qualify-release.sh --fixtures
```

Candidate `3f9c1bd` accepted that cache (exit 0). Candidate `21bf25e` rejected
the same cache (exit 1) at `Core cached module integrity`, before inventory or
builds, with `golang.org/x/sync v0.21.0: dir has been modified`.
Conductor receipts and full stdout/stderr are retained under
`.batuta/runs/2026-10-13-complete-journey/` in the parent checkout (untracked
conductor evidence): `core-cache-complete-canary.json`,
`qualifier-core-cache-complete-red.*`, `qualifier-core-cache-complete-green.*`,
`qualifier-mac-verified.*` and `qualifier-linux-complete-cache.*`.
These are fixture proofs; no check was weakened.
Rendered browser, outer Console PTY, clean-install bundle discovery,
standard-user Windows, real providers, stable/beta publication and whole
Task38 completion remain outside this command. Task43's 107 native failures
remain open. Historical evidence below is unchanged.

### Additional outer Console PTY selector

The inventory now additionally requires
`TestOuterConsolePTYCatalogApprovalAndRestart` on Darwin/Linux. The previous
20 required names and their historical 40 passing events above are unchanged;
those receipts do not include this new test. The existing inventory/pass-event
checks reject an absent name, a skipped test or a selection with zero tests.
All other qualifier stages, assertions and deadlines remain unchanged. Its
conservative final status line still leaves the full outer Console journey
unqualified.

```sh
go test ./internal/tui -run '^TestOuterConsolePTYCatalogApprovalAndRestart$' -count=1 -timeout=5m
```

This additional test builds current-source `console` and `consoled` into owned
temporary output and launches the actual Console with an 80-row, 240-column
outer PTY against its owned authenticated HTTP daemon. Disposable catalog
setup uses public HTTP; binding, source inspection, approval and the execution
attempt use real Console key bytes. The terminal engine decodes the visible
screen, reconstructing every displayed metadata/source byte before approval.
Independent HTTP reads assert the exact selected mission, binding and immutable
receipt while the other mission stays unchanged.

Both disposable provider CLIs only report their qualified version banners;
any other invocation leaves a failure marker. Empty isolated account vaults
must yield visible `credential_missing`, no sessions, no native opening or
delivery journal, and no model invocation. This is authentication refusal,
not successful official login or execution. No provider transport override,
readiness substitution or external provider request is used.

The test keeps Console alive while terminating the retained daemon process
with SIGTERM, requiring exit zero within eight seconds, then restarts the same
binary/catalog. The test sends public refresh/selection keys and verifies both
recovered mission identities on screen and through independent HTTP reads;
Rendered recovery and HTTP rereads preserve the exact binding/receipt and activity log without
repeated mutations. Console must exit zero through its public quit key. Output
readers and owned processes are joined before temporary-directory cleanup;
forced cleanup is a test failure, and unresolved cleanup retains its artifacts.
State/screen waits are bounded at ten seconds, and the overall work context at
240 seconds, within the five-minute test limit.

This covers only the outer-process catalog/approval/authentication-refusal and
actual daemon restart leg when run successfully. The existing
`TestJourneyPublicCoreQuestionResumeAndRestart` remains a separate model-driven
public Core question/resume test with local TLS/readiness injection and a
service/store restart. Neither substitutes for outer-process execution,
answer/resume or active-delivery stop. Rendered browser, clean-install bundle
auto-discovery, real official login/providers, native Windows (Task43's 107
failures), stable/beta publication and whole Task38 remain open. A sandbox
loopback/PTY/cache failure leaves this new runtime proof unverified.

The worker observed the required missing-name red (qualifier exit 1) before
adding the test. Its first targeted run compiled and freshly built both
binaries, then exited 1 on sandbox `listen tcp 127.0.0.1:0: bind: operation not
permitted`, before daemon/PTY startup. This is an environmental failure, not
behavioral TDD red or a runtime pass. The new journey and the final full
qualifier remain worker sandbox-unverified; no new native passing receipt is
claimed here.

The conductor subsequently ran candidate `9935e29` outside that sandbox:
the targeted outer PTY journey passed on native Darwin/arm64 in 17.77 seconds,
its harness race check passed in 19.59 seconds, and the full qualifier passed
on Darwin/arm64 in 99.82 seconds and Linux/amd64 (Docker Desktop, UID 1000)
in 54.07 seconds. Each full run had 41 passing test events without failures
or skips. These receipts are retained as `outer-mac-retry1.*`,
`outer-mac-race.*`, `outer-qualifier-mac.*` and `outer-qualifier-linux.*` under
`.batuta/runs/2026-10-13-console-public-flows/` in the parent checkout.

Independent Opus review then identified a background-refresh race. Controlled
model regressions showed that an automatic read could swallow an edited key
and erase a mutation refusal before rendering. Keyboard input now cancels only
an owned automatic journey read; foreground reads and mutations remain guarded.
Successful automatic reads preserve an active mutation failure notice until
an explicit successful inspection clears it. Canceled replies cannot replace
the edited draft. The qualifier also requires these two regressions.
Catalog/assertion calls use a ten-second client budget, while startup probes
retain one second. Binding checks use persistent rendered workspace authority
and exact HTTP rereads; authentication checks still require the visible error
and all no-execution assertions. No mutation retry or authentication bypass
was added. The 41-event receipts precede this fix and do not qualify it.

Candidate `7922a32` passed the updated full qualifier on native Darwin/arm64
in 67.59 seconds and Linux/amd64 in the same UID-1000 container in 54.09
seconds: each had 48 passing events, zero failures and zero skips. The owned
regressions reproduce both failures on the prior production source; the exact
same test bytes pass with the correction. The initial conductor test-authoring
compile error is retained separately and is not a behavioral red.
Receipts are `background-refresh-behavior-red.*`, `background-refresh-green.*`,
`background-refresh-frozen-red.*` and `critical-qualifier-{mac,linux}.*` in the
parent checkout's evidence directory above. The frozen comparison is a
supplemental rerun after the original red/green, not a new chronology claim.

A follow-up review found that the current notice's origin must be tracked
separately from mutation history. A rejected action followed by successful
inspection, a failed automatic read and a successful read must not restore
the acknowledged rejection. That sequence failed before the origin correction;
the same added regression is retained in `notice-origin-red.*`. Read failures
now carry their own error detail. Recovery restores an uninspected action
refusal; successful explicit inspection acknowledges it. The first read-failure
sequence failed in `notice-separated-red.*`; the green receipt covers both
sequences. A separate required regression covers uncertain action failure,
failed read and recovery without losing the uncertainty notice or original
error.
Its production source is unchanged from `a80f87f`: the controlled mutant red
is retained in `uncertain-notice-mutant-red.*`, and the race green in
`uncertain-notice-green.*`. Final candidate `c9b46ed` passed the qualifier on
Darwin/arm64 (84.38 seconds) and Linux/amd64 in the UID-1000 container
(53.78 seconds), each with 49 passing test events, no failures and no skips.
The corresponding receipts are `accepted-qualifier-{mac,linux}.*`.
The 48-event receipts
above precede this follow-up; they are not a final-head qualification claim.

## Evidence and current limits

The implementation starts from `f55edba0e0fd94ac408fba485d1531c3915ab848`.
The qualified source candidate is `513e4c1467ba20dd5ebdf978b5084c5c881ccecf`,
integrated into main at `2e092834a5f4a3dda341f2456ae909718836f792` after
independent Sonnet review accepted all six original Task42 criteria.
Native path red/green and macOS/Linux rechecks have been observed.
The native original full-store run still fails one protected-reader
prerequisite, which remains a Task43 obligation. This evidence completes
Task42 build portability; native Windows runtime, provider and release
qualification remain pending.

Conductor capture on 2026-10-08 used native Windows 11 Pro x64 build 26200,
private Go 1.27.1, Node 26.10.0 and npm 12.2.0. The verified Go archive SHA256 was
`a3911b5e0e1b1053f25ed0675f4c1c6aad1e2bfcf253df2b9be4caabd2edd95d`.
The module graph remains pinned to published Core `v1.1.0-beta.49`.

That **baseline** capture failed both command builds and independently compiled
19 package targets: 14 passed and five failed. Runtime files selected Unix APIs
on Windows; the limits test used `syscall.Mkfifo`. These are compiler failures,
not behavioral TDD evidence. An earlier PowerShell capture interrupted by
`NativeCommandError` remains an incomplete capture, not a completed profile.
The preserved conductor artifacts are under
`.batuta/runs/2026-10-08-windows-portability` in the parent checkout.

The conductor then froze the first submission as
`af8a7cabace08bc4865d4e05d654459042c73e35` (14 changed paths). All paths below
are relative to that parent evidence directory. JSON profiles retain exact
argv, exits, durations and SHA256 hashes of the retrieved raw streams.
The reviewed copies for this documentation continuation are in the worktree's
`.batuta/runs/windows-portability-docs-proof-completion`: `proof-summary.json`,
`final-candidate.json`, `final-scope-audit.json`, `protected-baseline.json`,
`native-tdd-verified.json`, `native-sdk-reverified.json`,
`current-profile-failure-judgment.json`, and the profile/raw files cited below.
`copied-proof-sha256.json` verifies those copies. The complete canonical
includeClosed response is preserved as `canonical.complete.txt` and all 13
`canonical-001.txt` through `canonical-013.txt` chunks, indexed by
`canonical.index.json`; its earlier pending-recheck entries are historical.
The completed captures below supply the current factual results without
changing canonical state or its acceptance gates.

| Capture on `af8a7ca` | Observed result | Evidence |
| --- | --- | --- |
| Native macOS | Full release profile exit 0, 500.32s; runtime/limits race count 5 exit 0, 55.02s; source guard clean | `mac-independent/profile.json`, `release-profile.{stdout,stderr}.raw`, `affected-race5.{stdout,stderr}.raw` |
| Linux, UID 1000 | Full profile exit 0, 451.42s; 2,930 Go test pass events, no test failures/skips; full race, affected race count 5, vet, terminal fixtures, both binaries, web 165, types, build/assets and module verification all exit 0; source guard clean | `linux-independent/status.json`, `summary.json`, `native-raws/results.json` and its named raw streams |
| Native Windows | Both command builds and all 19 package compilation commands exit 0; binary metadata and both `--version` commands exit 0; selected runtime/backend/path/setup/SQLite/file/TUI tests yield 437 passing test events and no skips, with one original SQLite test failing | `windows-independent/profile.json`, `summary.json`, `native-raws/*.log` |
| First native Windows web | Default 165 tests, types and build exit 0; overall native profile exit 2 and source guard dirty after CRLF-related generated-asset drift | `windows-independent/native-raws/web-*.log`, `asset-drift-diagnosis.stdout.raw` |
| Fresh exact-LF native Windows web | Default 165 tests exit 0, 85.37s; types/build exit 0 and source guard clean; profile exit 0 | `windows-web-exactlf/profile.json`, `summary.json`, `native-raws/web-*.log` |

The fresh checkout used command-scoped `git -c core.autocrlf=false` for
clone/checkout. No stored/global Git configuration or permissions changed.
The first dirty capture is retained; the later web proof does not turn the
first native backend profile into a passing full suite. That first full-store run
recorded 79 passing test events and one failing test,
`TestPlanApprovalExternalWriterAfterValidation` at
`internal/store/plan_approval_test.go:189`: `invalid catalog reference` before
secure IO. Its original approval/external-writer assertions were not reached.

Retry1 stage1 added only `integrations/batuta/plan_paths_windows_test.go` at
test-only checkpoint `00e693340f5eb55c2babc32b560b2365bf5a837d`. Its frozen
SHA256 is `046289a7a07815b624e0c5a41a879d60a71cc4b2d1698fcb191acb540e91ef11`.
The conductor observed native red with Go 1.27.1 using
`go test -json -count=1 -timeout=120s ./integrations/batuta -run '^TestWindows.*Plan'`:
exit 1, 43 passing test events, no skips, clean source guard. Four valid cases
(`active_slash`, `legacy_slash`, `legacy_core_native`, `done_native`) returned
`ErrInvalidReference` instead of `ErrArtifactIO`. All 33 invalid-path cases and
six direct safe-reader refusals passed. Evidence: `native-path-red/profile.json`,
`summary.json`, `native-raws/native-path-regression.{stdout,stderr}.log`.

Retry1 stage2 corrects only explicit catalog-location comparisons, accepting
Core's native spelling and its canonical slash spelling. The supplied path
still reaches the reader unchanged and remains `plan.Path`; no cleaning,
search, replacement, receipt/target/opening rewrite or secure-reader change is
introduced. Selected-source and archived-source comparisons use the same
restricted spellings so the existing competing-active-source check remains.
The conductor observed **native green at `513e4c1`**, exit 0 in 1.55s,
with the identical frozen body/SHA256 and the same 120-second argv as the red
(exit 1 in 1.86s). All six valid paths, 33 invalid paths and six direct safe-reader
refusals passed: 48 test pass events including the three parent tests, no test
skips or failures, and clean before/after source guards. Valid paths now reach
the unchanged unsupported reader and return `ErrArtifactIO`; they do not read
plan bodies successfully. Evidence: `native-tdd-verified.json`,
`native-path-green/profile.json`, `native-path-green/summary.json`, and
`native-path-green/native-raws/native-path-regression.stdout.log`, paired with
the preserved red files above. This completes the observed path regression
within stage2 of the same Task42 retry1 correction round, not a new code retry.

Stage2's earlier worker-local macOS check passed the complete `integrations/batuta`
and `internal/store` packages: 173 test pass events, exit 0 in 11.59s; race
count 5 produced 865 pass events, exit 0 in 48.04s, with no failures or skips.
The Windows integration test package cross-compiled, exit 0 in 7.70s. These
worker checks do not execute the frozen Windows-only test body natively.
Their argv, hashes and raw streams are recorded as `local-affected`,
`local-affected-race` and `windows-integration-compile` in the stage2 proof
directory below.

Worker evidence remains in `.batuta/runs/windows-portability`, with stage2
proofs in `.batuta/runs/windows-portability-retry1-implementation`. The original
worker `native-profile.json` was only a prepared run list; the conductor
captures above are the executed evidence. Original local failures on sandbox
socket binds and `TestNativeEscapedDescriptorCannotBecomeSuccessfulExecutorResult`
(probe startup not acknowledged), plus a denied read-only module-cache write
reported by a cross-build with exit zero, remain preserved. They are not
claimed as diagnosed or fixed. No worker web rebuild or frontend/dependency
change is part of stage2.

## Current conductor profiles and preserved failures

All current profiles in this section used source candidate `513e4c1` on
2026-10-08. Their JSON records contain the full head, exact argv, exits, timings,
raw hashes and empty before/after source status. None of these captures changes
the original Task42 acceptance criteria or closes QA38/41 reliability work.

| Capture | Observed result | Evidence |
| --- | --- | --- |
| Native Windows, current non-PTY profile | Both command builds and all 19 package compilation commands exit 0; module verification, binary metadata and both version commands exit 0. Selected tests: 485 pass events, no skips, one failing original store test. Profile exit 1, source guard clean. | `windows-final/profile.json`, `windows-final/summary.json`, `windows-final/native-raws/sqlite.stdout.log`; other named raw streams remain in the parent evidence directory. |
| Darwin/arm64, initial current profile | `sh scripts/check-release.sh` exits 1 in 187.50s at the original pending-group cleanup failure; source guard clean. | `mac-final/profile.json`, `mac-final/release-profile.stdout.raw` |
| Darwin/arm64, unchanged-source recheck | Same release-profile argv exits 0 in 454.21s; affected four-package race count 5 exits 0 in 111.01s; source guard clean. | `mac-final-recheck/profile.json`, `mac-final-recheck/release-profile.stdout.raw`, `mac-final-recheck/affected-race5.stdout.raw` |
| Linux/amd64, UID 1000, initial current profile | `go test -json ./... -count=1 -timeout=5m` exits 1 in 54.19s: 2,928 pass events, no test skips; timestamp subtest and its parent fail. Profile exit 1, source guard clean. | `linux-final/summary.json`, `linux-final/diagnosis.json` (failure output and original raw hash) |
| Linux/amd64, UID 1000, unchanged-source recheck | Full profile exit 0 in 449.96s; same full-Go argv exits 0 in 52.89s with 2,930 pass events, no failures or test skips. Full race exits 0 in 284.25s; affected race count 5 exits 0 in 52.14s; source guard clean. | `linux-final-recheck/status.json`, `linux-final-recheck/summary.json`, `linux-final-recheck/native-raws/full-race.stdout.raw` |

The current Windows full-store command is
`go test -json -count=1 -timeout=180s ./internal/store -run .`: exit 1 in
9.42s, 79 test pass events and one failed test, plus the package failure event.
`TestPlanApprovalExternalWriterAfterValidation` still fails at
`internal/store/plan_approval_test.go:189`, now with `artifact could not be read`
(`ErrArtifactIO`). Path recognition reaches the unsupported secure-reader
prerequisite; the original exact-read/external-writer assertions remain intact
and unfulfilled for Task43. **Neither the original full store nor the full
native Windows suite is green.** The broader profile's `plan-paths` selection
also passes 48 events, but uses `-timeout=180s`; the separate 120-second
`native-path-red`/`native-path-green` pair is the identical-argv TDD evidence.

The current Darwin release recheck includes normal Go tests (cached results
are explicitly visible for most packages), full race, vet, vulnerability scan,
web 165, types, build and asset checks. Its separate affected-race command is
`go test -race -count=5 ./integrations/batuta ./internal/store ./internal/runtime ./plugins/limits`.
Linux uses that same affected-race argv and
`go test -race ./... -count=1 -timeout=10m` for the full race profile. Its tooling,
module download/verification, pinned web install, vet, terminal fixtures
(`sh scripts/qualify-terminal.sh --fixtures`), both binary builds, web 165,
types/build, asset diff, Core provenance and binary metadata checks all exit 0;
the exact commands and hashes are in `linux-final-recheck/summary.json`.

Windows web evidence is reused from `windows-web-exactlf` at `af8a7ca`, not
represented as a new web run on `513e4c1`. Web source/tests, pinned graphs and
embedded assets are unchanged between those candidates. The previously
qualified default 165 tests, types/build and clean asset/source guard, together
with `native-sdk-reverified.json` (private Go 1.27.1, Node 26.10.0, npm 12.2.0),
remain the evidence. The original CRLF drift capture remains failed.

The initial Linux `TestEventReadFailures/live/timestamp` failure reported
`events_test.go:319` (expected close 1011, read returned no error) and line 322
(handler remained active). The conductor's unchanged-source diagnostic overlay
ran the actual handler: it delivered valid sequence 1 between the fixture's
separate `appendEvents` and `UPDATE`; after corruption, the exclusive cursor
had already consumed that event. This observed interleaving is recorded in
`linux-final/timestamp-diagnostic-file.stdout.raw` (exit 0, clean source guard).
Atomic fault publication in the original fixture remains mandatory QA38/41
work. No source or test correction occurred in the recheck.

The initial Darwin `TestProbeDeadlineAndNaturalExitDrainGroup/tree` failure
reported `probe_test.go:457`, `kill owned process group 72563: operation not
permitted` and `owned group exit is pending verification` during Shutdown.
The conductor's source-based inference in `current-profile-failure-judgment.json`
is that unchanged `signalOwnedGroup` emits `errGroupExiting` when inspection
finds `P_WEXIT` before a waitable zombie; retirement performs a final sweep and
retires authority while reporting unresolved cleanup. No kernel records from
the failing instant were captured, so this is not a confirmed kernel root
cause. The later `ps -p72563` observation at 13:51 found no current process;
it does not establish successful cleanup at the failed assertion. The conductor
issued no signals or permission/configuration changes. Pending group termination
still requires resolution and qualification in QA38/41.

Both later green profiles are unchanged-source qualification observations,
not fixes or reliability proof. The Linux publication failure, Darwin pending
group exit, prior spawn-EPERM, afterResize and lifecycle/UX obligations remain
mandatory; every failed capture remains preserved.

## Source-to-claim matrix

Observed Windows results below refer to `513e4c1`, except the explicitly reused
web capture and paired path red. Raw names refer to the parent evidence's
`windows-final/native-raws`, indexed with argv/exits/hashes by the copied
`windows-final/profile.json` and `summary.json`, unless another directory is
named. A refusal pass is not successful product operation.

| Surface | Source and native proof | Limit |
| --- | --- | --- |
| Runtime paths/auth/lock | `protection_windows.go`, `TestWindowsRuntimeProtectionRefusesWithoutMutation`; `runtime-refusal.stdout.log`, exit 0 | Unsupported without publication or source mutation. No ACL or lock qualification. |
| Lock read interface | `TestWindowsReadLockPreservesIdentityAndErrors`; same runtime log, exit 0 | Exact decoded identity, malformed/missing errors and durable source bytes. Metadata reading does not establish process ownership. |
| Daemon | `TestWindowsDaemonRefusesBeforeOpeningState`; `daemon-refusal.stdout.log`, exit 0; `actual-daemon-refusal.stderr.log`, expected exit 1; both version logs exit 0 | Refuses before SQLite/listener startup. No operational authenticated daemon claim. |
| Capabilities/HTTP | `TestWindowsCapabilitiesRemainUnsupported`, `TestCapabilitiesProvenanceAuthentication`; `capability-refusal.stdout.log`, exit 0; named authenticated TCP/typed/error/event selections in `http-typed-events.stdout.log`, exit 0, 41 pass events | Refusal/authentication and selected transport facts only; no operational capability or full backend-suite claim. |
| PTY/process/credentials | Existing `TestWindowsPTYUnsupported`; `pty-refusal.stdout.log`, exit 0; `credential_other.go` and session guards unchanged | No ConPTY, descendant containment, credential ownership or provider availability claim. |
| Setup and approved snapshot | Existing `TestSetupFileUnsupportedPlatform`, `TestPublishedApprovedSnapshotValidation`; `setup-snapshot.stdout.log`, exit 0, 14 pass events | Unsupported safe reads and snapshot validation only. No positive native plan-body read or approval proof. |
| SQLite | Entire original `internal/store` selection; `sqlite.stdout.log`, exit 1, 79 pass events and one failed test at line 189 with `ErrArtifactIO` | `TestPlanApprovalExternalWriterAfterValidation` remains deferred to Task43 with all original assertions intact. Full store is not green. |
| File imports | Existing size/concurrency/invalidation selections; `file-interfaces.stdout.log`, exit 0, 18 pass events | Production limits sources unchanged. Nonregular obligations below remain mandatory. |
| Picked paths | `TestWindowsNormalizePickedPaths`; `native-paths.stdout.log`, exit 0 | Drive root, UNC, Unicode, spaces and CRLF normalization; no interactive folder-dialog qualification. |
| Explicit plan paths | Frozen `plan_paths_windows_test.go`; `native-path-red` at `00e6933`, exit 1; same body/argv `native-path-green` at `513e4c1`, exit 0, 48 pass events | All six valid paths reach unchanged unsupported reader (`ErrArtifactIO`); 33 invalid references remain rejected before IO; six direct reader refusals pass. No positive secure-read claim. |
| TUI | Lossless journey bytes and pure terminal-render selections; `tui-pure.stdout.log`, exit 0, 275 pass events | Pure rendering only; no native terminal lifecycle claim. |
| Web | Default 165 tests, TypeScript and pinned build; first capture and `windows-web-exactlf` as above | Private Node 26.10.0/npm 12.2.0; first CRLF drift retained, fresh-LF guard clean. No frontend or dependency edits. |

## Test preservation and Task43 obligations

Unix auth and daemon-lock implementations retain their original bodies and
checks. Runtime home preparation is extracted without changing Unix operations
or their order. Build selection excludes these files on Windows. The retained
`TestWindowsRuntimeExcludesUnixImplementation` has an identical-body local
red/green for build selection; it is **not** native behavioral TDD. New native
refusal tests were authored before the runtime correction; native passing
results exist at `af8a7ca` and `513e4c1`, but their original behavioral red was
not observed on Windows. They are regression/qualification evidence. The
separate added, frozen plan-path regression has actual native red at `00e6933`
and identical-body/argv native green at `513e4c1`. The conductor's
`proof-summary.json` methodological judgment distinguishes those facts from
compiler red and host build-selection red/green; it does not claim a universal
native behavioral red. Worker-local claims do not replace that judgment.
Independent Sonnet review of all six original Task42 criteria and atomic
integration (`2e09283`) are complete; native Windows Task43 qualification
remains mandatory before operational support can be claimed.

The protected manifest contains 401 original tracked files, all still present,
including 190 test-related files: 181 Go `_test.go` files and nine web
test-related files. Of those, 189/190 test-related files and 180/181 Go test
files are byte-exact against the baseline. Only the original
`plugins/limits/read_test.go` changes: its FIFO constructor and descriptor-count
helper move behind platform fixtures. `protected-baseline.json` and
`final-scope-audit.json` record that distinction; the new frozen Windows
regression is additional and byte-exact across its native red/green pair.
The original test cases, subprocess watchdog, both Snapshot/All modes,
32 repetitions, provenance/measurement checks, leak equality and missing-file
checks remain. Darwin/Linux still use the original `Mkfifo(0600)` and descriptor
count implementation. No required test gains a skip or loses an assertion.

The `fifo`/`fifo-symlink` labels retain a Unix filesystem FIFO (`Mkfifo(0600)`).
Their Windows candidate is a filesystem AF_UNIX stream socket counterpart,
not a literal FIFO or a blocking `ReadFile` guarantee. [Microsoft documents
the bound socket as an NTFS reparse point](https://devblogs.microsoft.com/commandline/af_unix-comes-to-windows/).
The helper checks `ModeSocket`, `IO_REPARSE_TAG_AF_UNIX` and physical identity at
the original requested path, using a newly owned short parent alias under
`os.UserHomeDir()` only when needed. The listener has no acceptor or writer and
remains open until fixture cleanup. Existing controls and assertions are unchanged.

Go 1.27.2 `Stat` returns `ERROR_CANT_ACCESS_FILE` when following a file
symlink to an AF_UNIX socket. The Windows import reader now resolves metadata
for that error and accepts the result only when it identifies a socket. This
classifies the nonregular import as `Unknown` and invalidates a stale
measurement. The fallback never reads data; ordinary file reads are unchanged.

This follow-up adds only the `t` argument to the original FIFO constructor
call; reversing that call reproduces the entire prior `read_test.go` byte for
byte. The protected manifests above describe their historical capture.

Candidate source, test-first source and local command evidence are retained
under `.batuta/runs/2026-10-14-windows-beta1/nonregular-import-all-worker-raws`
and `native-host/` in the parent checkout. Qualify each candidate on native
Windows with the full original nonregular family using
`-race` plus `TestWindowsImportFilesystemEndpoint`, including short/long paths,
file symlinks, case/Unicode spelling, removal and alias cleanup. The original
32-read import loops retain exact descriptor-count equality. Fixture setup
verifies `SOCK_STREAM` after `Listen`; teardown requires a successful socket
close. A whole-process count around new test goroutines also counts Go runtime
thread resources and is not the fixture ownership oracle. The
Windows import counter now counts kernel File objects, including sockets and
pipes, via a current-process handle snapshot. Its exact equality is preserved;
a new native test requires real open-file/socket increments and return to the
baseline after close, while an Event must not count as a file descriptor.
The NT snapshot is test-only and fails on an unsupported query or invalid
layout. Darwin/Linux descriptor counters are unchanged.
macOS race results and Windows cross-compilation do not establish that
behavior. These privileged source fixtures do not qualify ordinary-user product
operation, the full Windows suite or beta readiness.

Before enabling Windows, Task43 must provide:

- Real blocking/nonregular replacement and named-pipe/reparse-point fixtures,
  including no-writer behavior, watchdog kill/reap, repeated reads, handle-leak
  detection, preserved provenance and stale-value invalidation. The Unix FIFO
  assertions remain required on Darwin/Linux.
- Kernel ACL/token/owner checks for the runtime home, accounts and secrets;
  reparse/hardlink rejection, single-linked regular files, concurrent token
  publication, interruption/partial-write recovery, durable exact bytes and
  exclusive daemon locks. POSIX mode bits are insufficient evidence.
- A qualified secure plan/setup reader, followed by the same original
  `TestPlanApprovalExternalWriterAfterValidation` assertions for exact read
  bytes, external-writer detection and approval/snapshot integrity. Fixing path
  recognition still reaches `errors.ErrUnsupported`, mapped by unchanged
  `artifactError` to `ErrArtifactIO`; it cannot qualify that original test or
  positive body/approval behavior. No weakening, skip or mock closes this gate.
- ConPTY startup/input/resize/signals/EOF/output ordering, process-tree ownership,
  cancellation, drain, escaped descendants/descriptors and restart/reconcile
  semantics. Core's other-platform direct-child cancellation does not establish
  descendant containment.
- The full native suite with original assertions intact, including the existing
  `internal/runtime/runtime_test.go` success contracts, original capabilities
  tests with Unix fixtures, credential checks, daemon lifecycle and hostfs tests.
  The pre-existing hostfs Windows skip and `false` executable fixture remain
  unchanged and need actual Windows equivalents; the new normalization test
  does not close those obligations.
- Current independent Darwin/Linux normal/race/runtime/API/terminal profiles;
  original spawn-EPERM, afterResize, event-fixture ordering and lifecycle/UX
  follow-ups remain mandatory QA38/41 work, outside Task42.

Task44 alone may exercise the authorized personal provider accounts. Task42
makes no provider calls and cannot close native security, provider, stable
release or publication gates. The parent conductor froze the candidate,
obtained the independent Sonnet review, updated canonical state and made
the atomic commit (`2e09283`).

## Disposable ordinary-user skills gate

The independent blocking `windows-ordinary-skills` job selects the same exact
`inputs.ref || github.sha` and Go 1.27.2. Before compiling the current service
tests, the parent runs full `go vet -tags console_ordinary_user ./internal/service`,
retains `vet.stdout`, `vet.stderr` and `vet.exit`, and fails on any nonzero exit.
It then compiles with `-tags console_ordinary_user` and requires exactly
`TestOrdinaryWindowsUserDiscoversSelectedSkills` and its Codex and Claude child
passes. The new ordinary-user test requires both `windows` and
`console_ordinary_user`; its complete body and assertions remain unchanged.
The default Windows suite runs on the hosted full-administrator runner and
retains every preexisting test, while this new test runs in the dedicated genuine
nonadministrator primary-account gate. The reusable CI workflow always requires
this independent job, including through the release paths; the tag selects the
actor-specific test at build time and introduces no runtime skip.

Only on GitHub's fresh hosted Windows 2025 VM, the parent creates a uniquely
named disposable local account with a cryptographically random in-memory
password. The [New-LocalUser contract](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/new-localuser)
accepts a SecureString; the [Start-Process credential/profile contract](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.management/start-process)
launches a genuine primary account process with its profile loaded. No linked,
restricted or synthetic actor is substituted. The child verifies its created
SID, native token type/elevation, groups and privileges, then runs the actual
compiled tests under an owned profile fixture with a protected user-only DACL.
The profile path must match the registry entry for the created SID; its owner
and DACL are recorded. A user, SYSTEM or Administrators owner is accepted for
this Windows-managed parent, without changing it. The new fixture still requires
that exact user as owner and its sole full-control ACL entry.
TEMP, TMP and LOCALAPPDATA point there. No SDK, provider login or model call is
needed in that account. Only the new staging directory grants the actor read
and execute access; existing ACLs, privileges and UAC settings are untouched.

Always-uploaded evidence includes source/tool/binary SHA256 inventories, raw
child and test streams, exits, exact test inventory and JSON events, native
identity/authority and fixture metadata, source guards and SID-matched cleanup.
The parent waits for the credentialed process tree to drain before removing
only its created account. Profile, staging and fixture files are left to the
hosted VM's disposal; no broad cleanup or arbitrary process killing occurs.
Missing metadata, failed/skipped tests, missing results or any nonzero test
exit block the gate. This bootstrap refuses developer and self-hosted machines.

On source `4c20ab17adebe0e69ef2243a4d38bdeb68389804`,
[run 38604819159, job 115992880955](https://github.com/batuta-ai/console/actions/runs/38604819159/job/115992880955)
passed the exact parent and both provider tests without failures or skips.
The genuine primary account was unelevated, had no Administrators membership
or symbolic-link privilege, and retained its two ordinary privileges. All five
selected roles per provider had actual directory/reparse attributes, exact
Readlink targets, matching kernel file identities and source bytes; the
unselected role remained absent. Source/tool/binary hashes stayed unchanged,
and the owned account was removed only after process-tree drain. Native
PowerShell parsing and independent Opus 5.5 source/criterion reviews also passed.
After the build-selection change, source
`2b30e9df1d86a5ab92f3cedd3227f5bcfa28a6f8` also passed
[run 38611127815, job 116014442657](https://github.com/batuta-ai/console/actions/runs/38611127815/job/116014442657):
full tagged vet exited zero, and the same three native cases passed without
failures or skips. Static Go 1.27.2 Windows selection retained all 166 preexisting
service root test names in the default build; the dedicated tag retained the
167-name baseline, including the new ordinary-user test. Its complete body
remains byte-exact after reversing only the header. GitHub's elevated default
runner cannot establish ordinary-user operation. The complete elevated Windows
suite still has unrelated failures; this selected actor proof does not establish
full Windows readiness, provider authentication or beta release readiness.
All other Task43 full-Windows obligations remain open.


## Pending run-owned MSYS temporary-directory qualification

The Windows caller harness now establishes a real Git MSYS keeper before the
complete, unchanged `go test ./... -json -count=1 -timeout=20m` invocation. It
checks `cygpath -w /tmp` against the caller's stable TEMP using native volume and
file identity, and retains the original process handle until Go and both output
streams have finished. It does not change that directory's ACL or remove it.
An unexpected existing mapping fails setup without touching foreign processes.

Before the suite, a synchronized counterfactual starts independent installed
MSYS actors A and B with distinct temporary roots. A exits and is joined before
its root is retired while B stays alive. The unchanged compiled literal test
must fail specifically because its otherwise exact result includes the missing
`/tmp` warning. The positive arm repeats that sequence and binary under the
caller's earlier keeper; the backing identity must survive and the original
literal test must pass. The same keeper then covers the actual suite. Original
inplace tests and both frozen TEMP tests, including `later_original_literal`,
must explicitly pass; unrelated failures and skips still block qualification.

The existing disposable ordinary-account route reuses its candidate test binary
and the same controller. It exposes read-only original source and the pinned
compiler, with private writable Go caches and process-local offline settings.
It retains the original skills and paired TEMP proofs, and additionally runs
both frozen TEMP tests, the original literal test and the original live-session
inplace test under the verified ordinary primary actor and whole-run lease.
These bounded operation fixtures do not replace the administrator source job's
complete original suite or its privileged adversarial cases. Raw streams, JSON,
exits, handle observations and before/after source/tool/binary hashes are retained.

This harness change has **not yet been natively qualified**. The recorded native
full-suite failure predates it; macOS inspection cannot establish PowerShell
parsing, the new controlled negative/positive result, ordinary-account runtime
success or a complete Windows pass. Those checks remain pending on Windows CI.
Task44 provider operation and release readiness remain open.

## Bounded Windows artifact qualification

`scripts/qualify-release.ps1` is a **source candidate; native proof is pending**.
It consumes the existing combined Windows amd64 ZIP and a separately trusted
`checksums.txt`. It never builds Console or downloads tools. The checksum proves
agreement with that manifest, not publisher identity: obtain both through the
release producer's trusted artifact channel. The existing `verify-packages.mjs`
contract remains unchanged. ZIP production and native CI wiring are a later task.

Run in a fresh native Windows x64 PowerShell 7.4+ process. Use an existing trusted
local NTFS staging parent and a new evidence target; do not change existing ACLs,
accounts, privileges or environment to make this check pass. Paths must be
canonical absolute drive paths, without UNC/device syntax, ADS, reparse points,
untrusted mutation rights or hard-linked input files. Parent owners may be the
current user, SYSTEM, Administrators or TrustedInstaller. The script creates
only its new directories with protected current-user-only DACLs. It refuses
unsupported ownership/NT query behavior rather than switching to PID-only kills.
No administrator bootstrap or ordinary-user qualification is included.

Example invocation (replace the artifact paths, version and full revision with
the exact producer inputs). The profile-local `ConsoleArtifacts` and
`ConsoleQualification` parents must already exist and be already trusted under
the path checks above; trust is not guaranteed by their location. The `run-001`
target must not exist. Any staging path with untrusted mutation rights is rejected,
including a child of `C:\` that inherits such rights; do not change ambient ACLs
to accommodate the qualifier.

```powershell
pwsh.exe -NoProfile -NonInteractive -File .\scripts\qualify-release.ps1 `
  -Archive "$env:USERPROFILE\ConsoleArtifacts\console_0.0.0-clean-install_windows_amd64.zip" `
  -Checksums "$env:USERPROFILE\ConsoleArtifacts\checksums.txt" `
  -ExpectedVersion '0.0.0-clean-install' `
  -ExpectedRevision 'd80b7ab289adf08091c131cfc748e3564593c4a6' `
  -Evidence "$env:USERPROFILE\ConsoleQualification\run-001"
```

Before starting a runtime, the consumer checks SHA256 and exactly one each of
`console.exe`, `consoled.exe`, `LICENSE`, `README.md` and `THIRD_PARTY_NOTICES`,
with no directory, link, duplicate, extra or renamed entry. It requires amd64 PE
images and both public `-version` diagnostics: exact release version/revision,
matching observed revision, explicitly unmodified source, actual `go1.27.2`
from `runtime.Version`, and unreplaced Core `v1.1.0-beta.49` in consoled. Console
may truthfully report that Core is not linked. The diagnostics are the executable's
reported provenance; this consumer does not independently parse Go module build
records with an SDK or qualify every transitive dependency. Package-wide build
record verification remains the producer's existing gate.

The real packaged Console runs three times with separate `-home <directory>`
and `-listen 127.0.0.1:<port>` arguments and redirected input/output. Its supported
nonTTY route must exit zero, emit no stderr and render the BATUTA Home navigation
and empty-workspace snapshot. The extraction directory contains spaces; the CWD
is a different empty owned directory. Each child receives a cleared environment,
an empty owned PATH, empty private profile/config/cache/temp locations, English
locale and `TERM=dumb`. Caller environment is untouched. No Go, Node, npm, Core
CLI or official provider is on that PATH; provider capability observations must
also report missing installations. No personal credential/config files are read.

The lock only locates a candidate process. Binding requires a retained process
handle, kernel image path, extracted executable hash, exact decoded argv, native
parent PID and creation time inside the original Console creator's lifetime.
Launch events label the already-hashed executable path as `requestedImage`, not
as a kernel observation: short-lived diagnostic processes may already have exited.
The live daemon's bound event retains the kernel image and hash proof.
Creator handles remain open through cleanup. Native process information is read
through those handles; [Microsoft documents the native parent query as subject
to Windows-version changes](https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntqueryinformationprocess).
The bounded NT command-line query must work on the target host; failure leaves
the process unqualified and forbids termination through its lock PID.

For each daemon generation, loopback-only HTTP checks require missing and wrong
Bearer tokens to return 401 on both capabilities and catalog routes. The correct
token must return the expected capabilities/provenance and the same fresh empty
workspace catalog. `/` must contain the production Console HTML, and its local
JavaScript and CSS references must serve substantive content with matching MIME
types. External font links are not fetched. No browser, `-web`, provider execution,
login or session is involved. The token is read only from the private owned runtime
and used in memory in authorization headers; it is never placed in a URL, command,
report or token hash.

Reinvocation must leave the original bound daemon object alive. The script then
terminates that idle object directly through its original handle, joins it, starts
Console again, and binds a newer daemon against the same home/listen/catalog and
unchanged secret. This is **direct termination / crash-restart, not graceful
shutdown**. Cleanup uses only retained Console objects and independently bound
daemon objects. It never kills a PID from a lock, process tree, name or unrelated
service. Recovery tries eligible original creator handles newest-first; when all
eligible bindings fail, it reports that no child was bound and that the lock may
be stale, with those errors preserved for inspection. A failed termination is
tolerated only when the same original handle
confirms exit; a still-live failure remains an error.
Primary failure stage/type/location are retained separately from cleanup
errors; raw exception payloads are withheld because they may contain captured data.

Limits are 180 seconds for the operation, with individual process waits of 15
seconds, HTTP requests of 5 seconds, ordinary async waits of 10 seconds and
hash/extraction waits of 30 seconds, all capped by the remaining budget. Cleanup
has a separate 20-second deadline and cancellation token, including recovery reads
and hashes even after the operation budget expires. Its joins and stream drains
are capped at 5 seconds and 1 second respectively and by the remaining cleanup
budget. Termination is still requested through each original owned handle after
that budget expires, with a zero-length join wait; a process not yet joined is
reported as a cleanup failure. Both cancellation sources are disposed. The
receipt records both phase
budgets and elapsed times; these timings end before the receipt is written.
Console stdout/stderr capture limits are 256/64 KiB; HTTP bodies are limited to
4 MiB and headers to 16 KiB. Checksums are limited to 64 KiB, the ZIP and total
expanded bytes to 512 MiB, each member to 256 MiB, and asset references to eight.
The private daemon log is checked against 64 KiB at lifecycle observations; it
is product-owned disk output, not a continuously enforced disk quota. Local OS
filesystem/native calls are synchronous; the time budget is not a watchdog for
an unresponsive kernel or filesystem. The released port reservation can race;
any resulting discovery/readiness/ownership mismatch fails qualification.

The only upload whitelist is:

- `receipt.json` (at most 4 KiB): outcome, narrow scope, expected provenance,
  source/artifact/member hashes, elapsed time and primary/cleanup diagnostics.
- `observations.json`: child launches/exits, snapshot byte counts/hashes, explicit
  provenance, isolation paths/DACL, bound process PID/creation/image/hash/argv,
  same-object reuse and joined termination events; HTTP path/status/content type,
  byte counts and content hashes, without response bodies or authorization values.

Do **not** recursively upload the evidence directory. Its retained `runtime home`
contains `auth.secret`, database and daemon logs; its other private subdirectories
contain extracted binaries and isolated process state. Nothing deletes those
roots automatically, even on failure. Dispose of the whole owned staging area
only after inspecting cleanup results and confirming no unbound process remains.
Neither raw process output nor HTTP payloads are public evidence.

Before accepting this candidate, native CI must execute actual-ZIP negative
controls for checksum, missing/duplicate/extra/renamed/traversal members, unsafe
paths and mismatched executable version/revision/Go/Core provenance, then the
positive against the unchanged original ZIP. Archive mutations must have updated
checksums so member rejection is actually reached. Provenance negatives must
reach the provenance check; a bad checksum is not that proof. Retain exact commands,
exit codes and the whitelisted receipts, and verify both daemon generations,
reuse, 401 controls and cleanup. Source inspection on macOS and the missing-script
red/sourceguard green do not establish parsing or behavioral TDD success. All
native negative/positive controls remain pending; no PowerShell was installed or
Windows bootstrap executed locally. This is only a Task38 artifact-consumer slice,
not interactive TUI, provider/council, Task43, full Task38 or release completion.

### Same-run snapshot CI bridge (native result PENDING)

`ci.yml` now adds `package-snapshot` on Ubuntu with the existing pinned Go,
Node, npm and GoReleaser versions. It checks the requested ref against HEAD and
requires a clean checkout before and after `sh scripts/package-release.sh`.
That unchanged producer builds all five snapshot archives, verifies their real
contents/build records and runs `package-contract.test.mjs`. No tag, release
publication or write permission is involved. `dist/metadata.json` must agree
with the manifest-derived snapshot version and full source revision.

The producer uploads the five public archives, checksums, GoReleaser metadata,
artifact inventory and `labels.json`. Labels bind the Windows ZIP/checksum
SHA256, qualifier/test source SHA256, revision, version, run ID and run attempt.
Its output binds the label SHA256 and upload artifact ID. The ordinary Windows
job requires successful producer outputs, downloads that exact same-run ID with
`digest-mismatch: error`, and checks labels against its own checked-out revision.
A failed producer fails the ordinary job explicitly; it cannot silently select
another artifact or yield a successful qualification through a skipped job.
Run-attempt binding requires re-running the full producer and consumer flow.
GitHub's "Re-run failed jobs" cannot reuse a successful producer from an earlier
attempt: its labels intentionally fail the ordinary job's attempt check. Use a
full workflow re-run; do not relax the binding or retry unchanged for green.

The existing disposable hosted-Windows account bootstrap stages these inputs
alongside the original source test binaries. Its before/after source and staged
hash inventory now includes the package inputs. The original ordinary skills,
TEMP baseline/candidate, literal and MSYS operations remain in place. After those
operations return, the existing ordinary actor copies only the bound inputs into
its own private fixture and verifies copy hashes and ownership. It invokes:

```powershell
& (Join-Path $bundleRoot 'qualify-release.test.ps1') `
  -Inputs (Join-Path $bundleRoot 'bundle-inputs.json') `
  -Evidence (Join-Path $bundleRoot 'public')
```

This is an internal invocation inside the existing actor, not a second account
bootstrap. Never run `check-windows-ordinary.ps1` locally. Each qualifier child
inherits that primary token without credentials, elevation or privilege changes;
the harness checks self-reported SID against the ordinary actor, PID and creation
time against the retained launching process object, and token type/elevation
fields against the required ordinary values. Separately, the wrapper rejects
Administrators group membership and SeCreateSymbolicLinkPrivilege; groups and
privileges are not compared with the launcher or actor. The
consumer remains byte-identical (SHA256
`98606f972b689f92b422c123c5bdea129c2ad649f3e3f85f5bc6f2c4643937b3`).

The native case order is counterfactual, checksum, missing, duplicate, extra,
renamed, traversal, relative path, ADS, dot-dot path, wrong PE machine, wrong
expected version, wrong expected revision, reported Go, reported Core, positive.
The duplicate replaces README with a second LICENSE, keeping five entries;
renamed/traversal also retain five entries. Modified archives get fresh checksums
so they reach the member/PE/provenance guard. Rejections require the intended
stage and exact frozen guard line, empty cleanup errors and the exact number of
version-only process launches; path cases must reject before evidence creation.
Go/Core cases replace equal-length data strings in copied actual PE images and
record image/ZIP hashes and replacement counts. They are **NOT different SDK
build qualification**. No fabricated executable or HTTP service is used.

The counterfactual changes exactly one checksum guard in an owned consumer copy
and supplies a wrong expected version to prevent runtime discovery. The unchanged
checksum assertion must become RED (the mutant reaches provenance with one
version launch); the following original checksum control must pass that same
assertion. The committed consumer and assertions are never relaxed. No retry,
skip or missing-script sourceguard counts as behavioral RED/GREEN. The final
positive uses byte-exact original ZIP/checksums and requires complete provenance,
two bound daemon generations, reuse, direct crash, eight HTTP 401 observations,
closed logs and joined daemon cleanup. Each qualifier is limited to 220 seconds,
with bounded join/drain waits and 64 KiB captured streams. The existing 20-minute
ordinary job timeout remains unchanged. On an outer timeout or capture error,
the harness attempts to stop only the retained original PowerShell process object
and waits at most five seconds for that process. It preserves the primary error
and records cleanup failures and stream drain status separately in the summary.
Descendant cleanup is explicitly unverified/incomplete on that path; joining the
PowerShell process does not prove daemon cleanup or artifact qualification.
Cancellation/disposal errors cannot replace the original error. Root must inspect
actual native case timings against the unchanged job budget before approval.

Additional public evidence names are an explicit finite whitelist:
`bundle-sourcebridge.json`, `bundle-copy-before.json`, `bundle-copy-after.json`,
`bundle-summary.json`, and `bundle-<case>-{actor,mutation,result}.json` for the
16 named cases. All except the three early path cases also require
`bundle-<case>-{receipt,observations}.json`. The parent retains every original
required record and rejects missing, duplicate or unknown transport records.
Raw qualifier stdout/stderr remain private; public result records carry their
hashes, arguments and exit codes. Only the public files are transported, never
recursive contents of the owned fixture, extracted package or runtime home.
The existing account drain/removal receipt remains mandatory.

Root must run and inspect CI on the exact candidate commit before approval.
Use the existing push/PR or reusable-workflow trigger; this worker does not push
or dispatch CI. After the authorized trigger, these read-only commands locate
and retrieve proof (replace the explicit placeholders):

```sh
gh run list --workflow ci.yml --commit <candidate-sha> --json databaseId,headSha,status,conclusion
gh run watch <run-id> --exit-status
gh run download <run-id> --name console-snapshot-<run-id>-<attempt>-event-ref --dir <owned-public-evidence>/producer
gh run download <run-id> --name windows-ordinary-skills-<run-id>-<attempt>-event-ref --dir <owned-public-evidence>/ordinary
```

These commands use the `event-ref` phase for push/PR defaults and reusable
callers without `inputs.ref`. A release callee with nonempty `inputs.ref` uses
`requested-ref`; replace the suffix in both download commands for that phase.
The three upload names (including `windows-source`) distinguish these two
phases within the same run/attempt even when both resolve to the same commit.
This covers the current two-call release contract; multiple callers in the
same phase are not guaranteed unique names.

Check all original source jobs and assertions, producer five-archive contract
logs/metadata, download digest outcome, source bridge and before/after hashes,
all 16 cases including original negative receipts, counterfactual RED, positive
observations, original CU token evidence and account drain/removal. Then obtain
an independent Opus review of the exact candidate and native evidence. Local
actionlint/schema/diff checks establish source consistency only. PowerShell
parsing, behavioral RED/GREEN, actual producer/native CI and independent review
are **PENDING** until Root verifies their real outputs. Task37/38/43 remain bounded
slices; native TUI/ConPTY, providers, full Task38/43, Task44 and release remain
open. Go 1.27.2 and Core beta.49 remain unchanged; there is no Core publication
or adoption in this work.

## Core beta.55 adoption and native runtime qualification — 2026-10-15

The current pinned release is [Core v1.1.0-beta.55](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.55),
released from commit `5c098af1d734af1e0dd8b6733f54faa78a2536da` following normal
[PR 173](https://github.com/batuta-ai/core/pull/173). Public Core
[PR 172](https://github.com/batuta-ai/core/pull/172) merged `7985226` after CI green
and administrator authorization. Module public checksum is
`h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=` and `go.mod` checksum is
`h1:4QhaQCQhu5s44DTpi7JdQrzceq1IqTSTDkb7HQxwPCk=`. The module checksum was verified
through the public Go proxy and checksum database. In separate comparisons against
Git, all 652 module files matched release commit `5c098af1`; the 306 Go files also
matched reviewed Core `c963`. Only those Go files were compared with the reviewed
commit. No replacement directive, global cache modification or CLI upgrade was performed.
Earlier dated Core 49 qualification records remain historical and do not qualify 55.

### Upstream Core CI and binary provenance limits

Core upstream CI ran clean on Linux Go 1.26.4; macOS Go 1.26.5 and Go 1.27.2 each passed
4,096 tests across 18 packages with `-race`. A known historical macOS Go 1.26.4 race timeout (15m)
cause remains unknown; newer SDK passes do not qualify older SDKs.
Core published binary archives for five targets have verified checksums matching GitHub API
digests, and metadata revision, version and platform match the release commit, but all record
`vcs.modified=true` (`v1.1.0-beta.55+dirty`). Exact build tree changes are not captured, and clean
source or reproducible build is not attested. Commit checksum and archive checksum are distinct.
These provenance limits are publicly documented in the Core release notes. Qualified stable Core
and plugin requirements remain unchanged future gates.

Current native runtime requirements supersede the earlier dated pin expectations.
On macOS and Linux, the Core CLI must be exactly `v1.1.0-beta.55` with the required
capabilities. `consoled -core /absolute/path/to/batuta` selects a private explicit
executable; otherwise `batuta` on PATH is used. Windows uses the Core SDK contained
in the Console image, with strict beta55 version, public module checksum and
no-replacement checks; it does not require an external Core CLI. Native Windows
qualification remains open. Earlier native package evidence does not qualify beta55.

### Console local bounded proof and unreleased status

Console's SDK adoption and strict native pins received review of the full change and
deltas. Bounded local checks covered service and integration tests with synthetic providers:

- 119 integration tests passed with `-race`; the unchanged concurrent-start
  regression passed all 20 executions.
- 56 Unix service-family tests passed; these do not qualify full native API/TUI operation.
- Windows strict metadata checks passed 10 subcases under an existing elevated SSH
  account only; Windows cross-build and `go vet` also passed.

These local bounded checks do not establish full Windows runtime support, real provider qualification, full web/TUI operation, or completion of Task38/43/44/Beta.
Console CI run [38754176954](https://github.com/batuta-ai/console/actions/runs/38754176954) on candidate `20ae827bbf5ebe72eb46cff986ce5956b92b7321` has finished: all 5 jobs failed. Linux and macOS native API and TUI fixtures still stamp Core 49 or lack capability response; the snapshot verifier escaped regex for 49 rejects 55; ordinary test runs are blocked by the failed producer; Windows also failed (3,530 passing and 22 failing test records, no skips), with detailed analysis pending. The four earlier Windows service failures involving Git exit 128 and Stop remain unresolved. Corrective compatibility work is ongoing.
Normal CI and bundle qualification remain open. Core publication is finished, but Console publication is not (`v0.1.0-beta.1` is not published or installed). No release-ready claim is made for Console.
