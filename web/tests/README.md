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

## Rendered Chromium catalog journey (partial Task38)

Requires Node 26.10.0, npm 12.2.0, Go 1.27.2 and the exact Playwright SDK
`@playwright/test@1.64.1` (including playwright/playwright-core 1.64.1).
From the repository root, use an owned browser cache, never an installed browser
or personal profile. Initial browser acquisition requires access to Playwright's
Chromium distribution; the test itself only contacts the owned loopback daemon.

```sh
export PLAYWRIGHT_BROWSERS_PATH="$PWD/.batuta/runs/rendered-browser/chromium"
export npm_config_cache="$PWD/.batuta/runs/rendered-browser/npm-cache"
npm --prefix web ci --offline --ignore-scripts --no-audit --no-fund
npm --prefix web exec -- playwright install chromium
npm --prefix web run test:e2e -- --list

# Select the prepared Go 1.27.2 SDK and populated, read-only module cache.
export GOROOT="/absolute/path/to/private/go"
export GOMODCACHE="/absolute/path/to/prepared/module-cache"
export GOCACHE="$PWD/.batuta/runs/rendered-browser/go-cache"
export GOENV=off GOTOOLCHAIN=local GOWORK=off
export GOFLAGS=-mod=readonly GOPROXY=off GOSUMDB=off
# macOS: canonical temporary directory outside every Git checkout.
export TMPDIR=/private/tmp
npm --prefix web run test:e2e

# Existing gates remain unchanged and need loopback permission where applicable.
npm --prefix web run test:run
npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json
PATH="$GOROOT/bin:$PATH" npm --prefix web run test:journey
PATH="$GOROOT/bin:$PATH" go test ./...
```

The single spec builds real consoled with its committed embedded production
assets; it does not run Vite or regenerate assets. Public APIs seed a disposable
workspace, two clearly labeled fixture accounts (no login, `cli_missing`), a
custom squad and its revision, and two missions with immutable snapshots.
At a fixed 1280×720 desktop viewport, the real UI opens the workspace card
inside the main landmark. In Missions, it checks each mission row’s exact ID,
title, objective and snapshot name before selection. In Room, it checks the
selected mission’s exact ID, title, objective and member models. Squad versions
and complete mission identities are checked through the public catalog. No
provider execution, route interception or substituted App is involved. A missing workspace profile is intentional; this slice navigates
existing missions and does not qualify workspace initialization.

The owned daemon must close and join with exit 0 before another process opens
the same home and catalog. The new process announces its actual loopback port;
authentication is read again and both unauthenticated rejection and authenticated
readiness are checked. A new private browser context selects both identities
again, eliminating retained DOM/storage as persistence evidence. Complete catalog
records are compared, excluding only account readiness `checked_at` timestamps.
Successful selections emit only nonsecret identity/version records.

Daemon and browser child environments are allowlisted with disposable HOME/TMP
and empty PATH. The daemon secret stays in the owned private runtime and browser
context memory: never use a token URL, storage-state export, request-header
recording, traces, screenshots, videos, HAR, debug protocol logging or uploaded
runtime directories. The config disables artifact capture and uses one worker,
Chromium only and zero retries. Build/start/read/action/drain deadlines are
bounded; readiness and exit events replace sleeps. Test-scoped teardown runs
also on failure; Playwright retains test and teardown failures separately.
Forced daemon cleanup is a failure. If joining or cleanup cannot be established,
the private runtime is retained instead of being deleted under a live process.

Native rendered proof is **PENDING until Root runs the actual browser command**.
The worker sandbox denies loopback. Listing or syntax checks are not rendered
proof; Root must run the rendered journey and rerun the unchanged unit and type
gates outside that sandbox. A prior Root browser run exposed an ambiguous
workspace selector; successful rendered qualification remains pending. This
desktop slice makes no responsive-layout qualification claim.

This is bounded catalog/navigation/restart coverage, not full Task38, native
execution, question/answer, resume, execution stop, provider qualification or a
beta release gate. The known SDK journal defect still blocks qualification of
the full execution flow even if this catalog journey passes.
