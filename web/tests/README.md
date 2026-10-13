# Web test foundation

Use Node 26 (Vitest requires Node ^22.12, ^24, or >=26):

```sh
npm --prefix web ci
npm --prefix web run test:run
npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json
```

`test:run` uses Vitest/jsdom and Testing Library user-event. It exercises the real
App, Lyra controls, and API helper: choose a workspace folder, edit its name,
submit, and observe the selected workspace and outgoing authenticated JSON.
Fetch responses, matchMedia, and WebSocket are isolated in-memory substitutes;
no server or provider is contacted. API tests assert response error propagation
and removal of a stale credential after a 401. Cleanup restores globals and
clears storage after every test. jsdom reports xterm's import-time canvas probe;
terminal rendering is outside this journey and the warning is not suppressed.

## Disposable real daemon journey

```sh
npm --prefix web run test:journey
```

Requires Go 1.27.2, Node 26.10.0, cached Go modules or access to download
them, and permission to bind loopback sockets. The script builds current
consoled in a temporary directory and launches it with a disposable
runtime/catalog, HOME and workspace, an empty PATH and no inherited credentials.
Authenticated HTTP checks cover:

- Unauthorized access rejection, workspace creation/listing, all original
  init-negative validation/readiness cases, and missing/partial/ready
  configuration. Profile/routing/WORK files are disposable fixture inputs;
  successful Workspace init is not exercised.
- Distinct Codex and Claude accounts, connection to distinct isolated vault
  directories, rename/listing and missing-CLI readiness.
- A custom squad and its revision, two missions capturing different squad
  snapshots, and selection by mission identity independent of list order.
- Stopping and restarting the same built consoled as an owned child against
  the same runtime/catalog. Restart requires a graceful zero exit; a nonzero
  exit, signal termination or forced kill fails before the second instance
  starts. The second instance must also stop gracefully. Authenticated rereads
  compare workspace/setup, accounts/vault references, squad and both complete
  mission records. Only the dynamic account readiness `checked_at` timestamp
  is excluded.
- Empty sessions before and after restart; no login, successful init, native
  mission start or model execution. Catalog writes use real endpoints, with
  no direct database writes or substituted handlers.

Authentication uses only the daemon's fresh temporary `auth.secret`. Owned
processes and data are cleaned up on success, failure, SIGINT and SIGTERM.
Build, startup, API calls and graceful-stop escalation have bounded deadlines;
startup waits on the daemon's listening message, without readiness sleeps.
The five-second stop fallback still kills and reaps a stuck owned child;
cleanup removes disposable data even when stopping fails and preserves the
original journey error if cleanup also fails.
The ephemeral port probe releases its port before consoled starts; a competing
bind fails startup rather than connecting to another process.

This is **synthetic API qualification using a real daemon**, not a rendered
browser or official provider login/model proof. Restart verifies catalog
persistence, not recovery of active model execution. To also run the existing
TUI/Core/PTY/HTTP fixtures, use `sh scripts/qualify-release.sh --fixtures` from
the repository root; see [release qualification](../../docs/release-qualification.md).

Testing dependencies are pinned to the latest verified releases for this foundation:
Vitest 5.0.3, Testing Library React 16.3.3, DOM 10.4.2, user-event 14.6.7,
jest-dom 7.0.1, jsdom 30.1.1, and @types/node 26.6.4.
