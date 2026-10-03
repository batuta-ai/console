# Release-readiness evidence

Audit: 2026-10-02, Console `0461003`, local core `b33cc9a`. Host: macOS arm64. Project Go: 1.26.4; Node: 26.9.0; npm: 11.19.1. See [the findings and release sequence](release-readiness.md).

The starting Git tree was clean. The web build regenerated exactly the existing embedded assets. The independent reviewer was restricted to Read/Grep/Glob, with file-edit tools disabled; its before/after Git-status guard was unchanged. Audit artifacts were written after that review. No implementation fixes, commits or publication were performed.

## Build, static checks and ordinary tests

Executed from the Console root unless otherwise indicated:

```sh
go test ./...
go vet ./...
go build -o /tmp/batuta-console-audit-consoled ./cmd/consoled
go build -o /tmp/batuta-console-audit-console ./cmd/console
```

All passed. In `web/`, `npm ci`, `npm run build` and `npx tsc --noEmit` passed. The build produced:

```text
dist/index.html                    0.98 kB | gzip: 0.54 kB
assets/index-BufCjKud.css         135.66 kB | gzip: 21.74 kB
assets/index-CDgUSuU8.js          679.98 kB | gzip: 184.89 kB
Some chunks are larger than 500 kB after minification.
```

`git ls-files internal/webui/dist` confirmed that the embedded HTML, JS, CSS and symbols are tracked. The rebuilt names/content did not change Git status. Claims that these assets are ignored or that a normal fresh checkout necessarily serves a diagnostic page were rejected.

## Published core dependency

`go mod download -json github.com/batuta-ai/core@v1.1.0-beta.21` resolved the published tag successfully. A copy of `go.mod`/`go.sum` under `/tmp/batuta-console-audit-release/` removed only the sibling replacement, and this passed:

```sh
go test -mod=mod -modfile=/tmp/batuta-console-audit-release/go.mod ./...
```

This disproves an assumed incompatibility with the declared beta.21 dependency. It does not validate an unspecified future stable tag. The checked-in replacement still prevents a normal standalone build without the sibling and must be removed from release module metadata.

## Race and SQLite checks

```sh
go test -race ./...
```

Failed in API and session test packages. The API failure was:

```text
--- FAIL: TestNaturalExitIsExited
server_test.go:359: /v0/sessions: 500
{"error":"store","message":"database is locked (5) (SQLITE_BUSY)"}
```

The confirmed race was in a test's shared callback result:

```text
WARNING: DATA RACE
Write: internal/session/manager_test.go:157, TestOnExitPersists.func1
Read:  internal/session/manager_test.go:163, TestOnExitPersists
--- FAIL: TestOnExitPersists
race detected during execution of test
```

The focused check then reproduced the API error five times:

```sh
go test -race ./internal/api -run TestNaturalExitIsExited -count=5
```

Result: five `SQLITE_BUSY` failures, exit 1. The production start/exit write-order risk is a separate source finding; the race-detector output above must not be described as a production session-field race.

## Coverage

```sh
go test -coverprofile=/tmp/batuta-console-audit-coverage.out ./...
go tool cover -func=/tmp/batuta-console-audit-coverage.out
```

The coverage run passed and measured 44.9% overall. Selected packages: API 48.4%, service 48.8%, store 31.2%, TUI 15.4%, session 81.3%, Batuta integration 68.4%. There is no web test script/framework in `web/package.json`. A successful coverage run does not override the failed race/concurrency checks.

## Platforms

```sh
GOOS=linux GOARCH=amd64 go build -o /tmp/batuta-console-audit-consoled-linux ./cmd/consoled
GOOS=windows GOARCH=amd64 go build -o /tmp/batuta-console-audit-consoled.exe ./cmd/consoled
```

Linux cross-build passed. Windows failed:

```text
internal/runtime/runtime.go:106:20: undefined: syscall.Flock
internal/runtime/runtime.go:106:47: undefined: syscall.LOCK_EX
internal/runtime/runtime.go:106:63: undefined: syscall.LOCK_NB
```

The Windows PTY source separately returns an unsupported error. Linux runtime, Windows runtime, native directory dialogs, official CLIs and an interactive TUI were not qualified by these compile checks.

## Dependency scans

`npm audit --json` reported zero vulnerabilities in the resolved web graph (125 dependency entries in the audit metadata).

`govulncheck` was installed into an audit-only temporary bin directory from `golang.org/x/vuln/cmd/govulncheck@latest` (resolved x/vuln v1.8.0), and scanned the project with `govulncheck ./...`. Result: exit 3, six reachable-symbol findings in the Go 1.26.4 standard library:

| Report | Package/issue | Fixed on Go 1.26 branch |
| --- | --- | --- |
| [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) | net/url relative-path resolution complexity | 1.26.6 |
| [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) | crypto/tls post-handshake message limit | 1.26.6 |
| [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) | net/http unencrypted HTTP/2 preface timeout | 1.26.6 |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | encoding/asn1 recursion depth | 1.26.6 |
| [GO-2026-5856](https://pkg.go.dev/vuln/GO-2026-5856) | crypto/tls ECH privacy | 1.26.5 |
| [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) | IDNA validation reached through net/http | 1.26.6 |

The scanner also reported two imported-package and two required-module findings without apparent calls to the vulnerable symbols. Those were not individually triaged in this audit. The primary Go database reports for net/url, HTTP and TLS were opened and checked. Reachable static symbols are not proof of exploitability in the daemon's current plain loopback HTTP deployment. Update the build toolchain and rerun the scan rather than weakening its checks.

## Live API/PTY experiments

A temporary daemon used a new catalog/home, an ephemeral loopback port and fake Codex/Claude executables that only printed a marker and slept. Requests authenticated using that disposable daemon's token. No user's account directories or real model APIs were used. The following results came from actual HTTP requests and PTY processes:

| Experiment | Observed result |
| --- | --- |
| `/healthz` without token | HTTP 200. |
| `/v0/accounts` without token | HTTP 401. |
| Create mission with workspace `does-not-exist` | HTTP 200; invalid reference stored. |
| Start bound Codex account with `connected: false` | HTTP 200; process started. |
| Configure member model `audit-model`, effort `high`; inspect session argv | Only executable and planning prompt; neither configured setting appeared. |
| Start without explicit member ID | Default member resolved, but returned/stored member ID empty. |
| Start Codex with a Claude account ID | HTTP 200; session adapter Codex, account adapter Claude. |
| Start member `does-not-exist` | HTTP 200; invalid member retained, default Codex selected, account omitted. |
| Start absent Cursor executable | HTTP 200; adapter Cursor, argv `/bin/sh`. |
| Set daemon synthetic `OPENAI_API_KEY=audit-fake-credential`; test for it inside account session | The fake key was inherited. Only the boolean/marker was inspected, not real credentials. |
| Ten parallel starts on Codex account cap 3 | Nine accepted; statuses 9×200, 1×400. |
| Connect OpenCode/Gemini/Cursor account folders | All reported connected=true, authed=false; source makes authed=false unconditional for these adapters. Real official login was not attempted. |

The capacity experiment used a synchronization barrier for simultaneous request dispatch and `/bin/sleep` processes, then stopped accepted sessions. It establishes a check/start race independently of provider rate limits.

### Stop and restart

The child-stop fixture launched:

```sh
/bin/sh -c "trap '' HUP; /bin/sleep 60 & printf 'CHILD_PID=%s\n' \$!; wait"
```

After the API stopped the leader, the printed child's PID remained alive. The audit explicitly killed its own child afterward.

For restart, three cap-filling sleep sessions were started, the temporary daemon terminated, and another daemon opened the same temporary catalog. Before any session-list call, the next start returned:

```json
{"error":"session","message":"account at session capacity 3"}
```

After `GET /v0/sessions`, the same new start returned HTTP 200. Remaining test processes and the replacement daemon were terminated. This proves stale capacity recovery depends on a client GET; it does not assert that the product must resurrect a PTY after restart.

## Small integration harness

A separate temporary Go module imported the Console and local core through audit-only replacements. It did not edit the production module or add repository tests.

With `BATUTA_SKILLS` unset, Console `Prepare` selected the host `.agents/skills` parent and reported enabled=true. Passing that selection to core `loop.FindSkills` returned:

```text
loop: the batuta skills are not installed (no adapters/ directory found);
pass --skills <dir> or run the installer
```

A seven-day-old synthetic `rate_limit.json` containing percent 42/window 5h was read as source=current, with observed_at equal to the current read time. A valid synthetic auth secret explicitly chmodded to 0644 was loaded successfully and retained mode 0644. Both are confirmed fixture results; concurrent first-token creation was not tested.

## Independent review adjudication

A separate Claude `sonnet` CLI reviewed source with Skeptic, Architect and Minimalist lenses. Its raw `<<<FINDINGS` block is retained locally in `.batuta/runs/2026-10-02-release-readiness.review.md`. The session ended normally with exit 0; read-only status guard passed. Each of its thirteen findings was judged as follows:

| Reviewer finding | Judgment |
| --- | --- |
| go.mod sibling replace prevents standalone publication | Accepted — R01; published beta.21 test supplements the finding. |
| Stop kills only the leader | Accepted — R09; live child fixture reproduced it. |
| Restart must resurrect/kill every stored PID | Partially accepted — R09/R11 require honest cleanup/recovery; automatic PTY resurrection is not required by the documented catalog-only contract, and blindly killing stored PIDs is unsafe. |
| Fast exit can be overwritten by initial running write | Accepted as source-supported risk — R11; not claimed separately reproduced. |
| Stale running rows block capacity until GET | Accepted — R11; live restart reproduced it. |
| Mission watch can select another delivery | Accepted — R05; core List was checked and sorts newest first, which still does not associate a delivery with a mission. |
| Answer lacks delivery/question identity | Accepted — R06; core's exact-question validation was inspected. |
| Isolated HOME makes host skills/Git tools unavailable | Partially accepted — R04/R07 need host and environment qualification; core path rejection is reproduced, but real CLI skill loading/Git authentication failures were not tested. Do not indiscriminately share host configuration. |
| Non-Codex/Claude adapters lose positional objective | Accepted — R14; source contract is explicit. |
| Windows PTY unsupported | Accepted — R18; compile failure is an additional earlier blocker. |
| Shutdown waits for hijacked WebSockets forever | Rejected as stated — Go HTTP Shutdown does not manage hijacked connections. Missing explicit session/socket shutdown policy remains in R09/R22. |
| Resolved member ID is not persisted | Accepted — R12; live default-member response had no ID. |
| Embedded dist is ignored and ordinary builds show a diagnostic page | Rejected as stated — dist is tracked and rebuilt identically. Missing repeatable release automation/version diagnostics is accepted separately as R19; missing embed-directory fallback documentation is corrected in R25. |

The first reviewer invocation had a CLI argument-parsing error and performed no review; a corrected invocation completed. The successful result and guard, not the failed attempt, underpin this adjudication.

## Reproduction limits and setup state

No enabled browser was exposed by the computer-use inventory. Visual/accessibility certification, browser reconnection and new-user interaction scenarios therefore remain acceptance work, not completed tests. The full plan/approval/core-loop workflow, provider login, Linux runtime and real quota-file production were not executed.

Batuta init configuration and routing were proposed through two asynchronous questions. No reply had arrived when this report was prepared. Its skill explicitly requires confirmation before writing the routing table; the profile/routing/WORK self-check was consequently not claimed complete. Review artifacts can exist under `.batuta/runs` without certifying project setup.


## 2026-10-03 remediation evidence — R02 toolchain qualification

The October 2 evidence above remains unchanged. The conductor supplied the pre-edit regression scan under Go 1.26.4: `govulncheck ./...` exited 3 with six reachable standard-library findings, plus two imported-package and two required-module findings without apparent vulnerable-symbol calls. The supplied red output is retained in `.batuta/runs/go-toolchain-2026-10-03/baseline-red.txt`.

The conductor independently qualified source snapshot `43c49ec` under Go 1.27.1 with matching SDK environment. These are supplied host results, not worker reruns. The Go directive and README minimum were updated; dependency requirements and `go.sum` are unchanged.

| Check | Independent conductor result | Retained parent log |
| --- | --- | --- |
| `govulncheck -show verbose ./...` | Exit 0; scanned 17 root packages, 35 modules and Go 1.27.1; **No vulnerabilities found** | `.batuta/runs/2026-10-03-go-toolchain.green-vuln.log` |
| `go test ./...` | Exit 0; all 14 tested packages pass; `cmd/consoled`, accounts and webui have no tests | `.batuta/runs/2026-10-03-go-toolchain.green-test.log` |
| `go vet ./...` | Exit 0; no output | `.batuta/runs/2026-10-03-go-toolchain.green-vet.log` |

All six reachable R02 findings disappear, as do the baseline package/module reports; no remaining findings require triage at this checkpoint. No checks were weakened or findings suppressed. Worker sandbox and SDK troubleshooting remains in managed run evidence under `.batuta/runs/go-toolchain-2026-10-03/`.

Source qualification of R02 passes. Final CI/release metadata qualification and release binary rescans remain future release tasks. Existing SQLite/lifecycle race findings remain open; the passing test command is not race-detector qualification. This evidence does not establish whole-release readiness or mark the plan complete. The final host tree remains subject to independent review.
