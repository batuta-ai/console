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

On Windows, `makeImportFIFO` deliberately returns an explicit unsupported-fixture
error. It does not substitute a directory or claim a named pipe is a filesystem
FIFO. Thus both `fifo` and `fifo-symlink` remain failing obligations in the full
native suite. The directory, directory-symlink and regular-symlink cases still
exercise their original assertions, using actual process handle counts for leak
checks. Native qualification must establish symlink privileges and handle-count
stability without changing global permissions or weakening those checks.

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
`inputs.ref || github.sha` and Go 1.27.2. It compiles the current service tests
with `-tags console_ordinary_user` and requires exactly
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
That run qualifies the existing native actor gate on the recorded source; it
does not prove the candidate after the build-selection header change. Fresh
caller-owned native proof of the frozen candidate must still confirm the exact
parent and two child passes, and default Windows inventory must retain all
preexisting names while omitting only this new ordinary-user test. GitHub's
elevated default runner cannot
establish ordinary-user operation. The complete elevated Windows suite still
has unrelated failures; this selected actor proof does not establish full
Windows readiness, provider authentication or beta release readiness. All other
Task43 full-Windows obligations remain open.
