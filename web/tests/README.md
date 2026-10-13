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

Requires Go 1.27.2, cached Go modules or access to download them, and permission
to bind loopback sockets. The script builds the current consoled in a temporary
directory, launches it with a disposable runtime/catalog, HOME and workspace,
and a credential-free environment with an empty PATH. It checks unauthorized
access rejection, workspace creation/listing, account connection to its isolated
directory, account rename/listing, and absence of sessions. Only catalog APIs
are used: no login, workspace initialization, model execution, or CLI is invoked.
No fake provider CLI is installed. Authentication uses only the daemon's fresh
temporary auth.secret. Processes and data are removed on success, failure,
SIGINT, and SIGTERM. Startup and API calls have bounded deadlines.

This is **synthetic API qualification using a real daemon**, not real browser
qualification or proof of official Codex/Claude login, credentials, or model
execution. Those remain later release qualifications. The ephemeral port probe
releases its port before consoled starts; a competing bind produces a startup
failure rather than connecting to another process with personal credentials.

Testing dependencies are pinned to the latest verified releases for this foundation:
Vitest 5.0.3, Testing Library React 16.3.3, DOM 10.4.2, user-event 14.6.7,
jest-dom 7.0.1, jsdom 30.1.1, and @types/node 26.6.4.
