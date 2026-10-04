# Account credential environments

Console closes the environment at the final process launch in the session
Manager. Session, login and project-init launches use the same policy. The
reusable `accounts.RuntimeEnvironment` policy also applies to future execution
paths: a subprocess must not reconstruct its environment from the full daemon
`os.Environ()` afterward.

## Inherited runtime

Only these daemon variables may pass to a child:

- `PATH`, `LANG`, `LC_ALL`, `LC_CTYPE`, `LC_COLLATE`, `LC_MESSAGES`,
  `LC_MONETARY`, `LC_NUMERIC`, `LC_TIME`, `LC_ADDRESS`, `LC_IDENTIFICATION`,
  `LC_MEASUREMENT`, `LC_NAME`, `LC_PAPER`, `LC_TELEPHONE`.
- `TERM`, `COLORTERM`, `SHELL`, `DISPLAY`, `WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR`,
  `DBUS_SESSION_BUS_ADDRESS`.
- `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS`, `CODEX_CA_CERTIFICATE`.

Console fixes `TERM` to `xterm-256color`. Explicit internal runtime entries take
precedence over inherited entries; duplicate explicit entries use the last
value. Names are matched exactly. Unknown variables, provider keys/tokens,
provider endpoint/mode selectors, credential helpers, shell startup files and
runtime injection controls such as `NODE_OPTIONS` do not pass. The same closure
rejects those keys from explicit internal environment slices.

`HOME`, `TMPDIR`, XDG config/data/cache/state homes and provider config homes
come only from Console's internal account/session preparation. Daemon values for
these keys are never inherited. A supplied internal `HOME` is a trusted caller
capability; the HTTP API does not accept environment overrides. Service account
launches validate and prepare their directories before handing them to Manager.

Only application-created `BATUTA_CONSOLE`, `BATUTA_WORKSPACE`, `BATUTA_SKILLS`,
`BATUTA_JOURNAL`, `BATUTA_ROLE_SKILLS`, `BATUTA_MISSION`, and `BATUTA_INIT`
metadata may pass. A daemon variable with one of those names does not pass
unless the application explicitly constructs it for this launch.

## Account homes

An account lives under `<console home>/accounts/<account ID>`. The Console home
must be absolute, nonempty and distinct from the filesystem root. Account IDs must be nonempty single path
components, excluding dot/dot-dot, either path separator and NUL. Connection
and launch propagate directory failures; they never fall back to the daemon's
personal home. An account-bound session requires an existing account whose
adapter matches the selected adapter.

The account home, `tmp`, `.config`, `.local/share`, `.cache`, `.local/state` and
selected provider directory are created with mode `0700`. Existing account subdirectories are checked and tightened to that mode.
Account setup checks an existing Console home without changing its permissions. Symlink and nondirectory
components at the Console home or controlled account/config paths are refused.
Account setup does not delete existing credentials or account homes.

Codex receives `CODEX_HOME=<account home>/.codex`. Claude receives
`CLAUDE_CONFIG_DIR=<account home>/.claude`. Cursor's existing directory control
uses `<account home>/.cursor`; this does not qualify Cursor for the first stable
release. Other provider selectors from the daemon are absent. No personal
credential file is searched or copied during environment construction.

An authenticated-account init launch retains its selected account environment.
The existing init selection and recorded account/capacity contracts are handled
separately by the init qualification task. This environment change does not
establish that a file-presence authentication predicate proves a valid login.

## Sessions without an account environment

A direct or generic launch without an explicit internal account home receives a
fresh `0700` temporary home. Unbound init fallback follows this same path. Its
config/data/cache/state/temp and Codex/Claude/Cursor homes are below that new
home. It starts without credentials from an earlier session, account or daemon.

Manager removes only the temporary tree it created, after actual owned process
collection, exit callbacks, PTY reader and log cleanup. Failed starts remove
that same owned tree and release admission. Persistent account directories are
never removed by session cleanup. Creation and cleanup failures remain errors;
an unresolved private-home cleanup failure is retained with unresolved owned
process cleanup failures. Shutdown still covers admitted, unpublished starts.

An abrupt daemon termination, such as a crash or `SIGKILL`, can leave its
private temporary homes behind. Those directories remain private, and a new
session uses a fresh home. Startup does not adopt or automatically delete these
orphaned directories. Cleanup guarantees apply to completed session lifecycles
and graceful shutdown.

## Limits and qualification

This is environment and account-directory selection, not an OS or filesystem
sandbox. A CLI runs as the current user and may access files, project settings,
explicit command/config paths, launchers and operating-system credential stores.
The checks do not establish protection against hostile concurrent mutations by
another process running as that user. Runtime launchers requiring additional
variables must be qualified explicitly, rather than inheriting arbitrary daemon
configuration.

Tests use named synthetic variables, disposable CLIs and synthetic credential
identities for Codex and Claude accounts A/B. They do not read personal
credentials, invoke the real providers or establish provider readiness.
Real Codex/Claude authentication, configuration precedence and complete web/TUI
journeys are qualified separately on macOS and Linux before stable publication.

Provider directory contracts were checked against official documentation:
[Codex authentication](https://developers.openai.com/codex/auth/),
[Claude environment variables](https://code.claude.com/docs/en/env-vars), and
[Claude authentication](https://code.claude.com/docs/en/authentication).
