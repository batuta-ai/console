# Official PTY command contract

`Adapter.Command` constructs interactive, project-init and explicit login commands
for Codex and Claude. Other registry entries describe metadata and isolation; they
are not supported command providers. `ErrUnsupportedCommand` identifies unsupported
providers or modes; `ErrInvalidCommand` identifies invalid configuration. Executable
lookup failures wrap the original `exec.LookPath` error.

The syntax baseline is the conductor's captured Codex 0.160.0 and Claude Code
2.1.286 and 2.1.289 help (2026-10-04). The 2.1.289 command list adds `purge`,
which is also refused as a positional goal:

On 2026-10-13, Codex 0.161.0 main, exec and login help retained the syntax in
the table below. Its JSON exec interface returned the expected response using
an explicitly selected local account in an isolated HOME. The real daemon's
fresh provider check then reported that account ready. This adds exact-banner
readiness support; full interactive provider and Console mission qualification
remain pending. Captures are `codex-0161-*-help.txt`,
`local-codex-live-preflight.json` and `codex-0161-real-readiness.json` under
`.batuta/runs/2026-10-13-console-public-flows/` in the parent checkout.

| Input | Codex | Claude |
| --- | --- | --- |
| Goal | One positional argument | One positional argument |
| Model | `--model MODEL` | `--model MODEL` |
| Effort | `--config model_reasoning_effort="LEVEL"` | `--effort LEVEL` |
| Login | `codex login` | `claude auth login` |

Codex effort syntax accepts `low`, `medium`, `high`, `xhigh`, `max`, `ultra`.
Claude accepts the same list except `ultra`. The OpenAI
[configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
documents `model_reasoning_effort`; the Claude
[CLI reference](https://code.claude.com/docs/en/cli-reference) describes interactive
prompts and model/effort flags. Installed help, rather than newer documentation,
determines the qualified syntax here. These lists do not qualify model availability,
authentication or model/effort compatibility; the provider must report runtime errors.
No registry model allowlist is applied.

Nonblank values are passed literally, without shell interpolation. Whitespace-only
model/effort inputs omit flags. Other effort strings, NULs and option-prefixed model
or goal inputs fail before spawning. A goal equal to a command name or alias in the
qualified help also fails explicitly, avoiding accidental subcommand dispatch.
Project-init and injected mission prompts retain their existing instructions; a
blank mission goal produces no fabricated instructions. Generic goals retain their
original text.

An explicit custom argv is allowed only for interactive Codex/Claude identities
without a resolved nonblank model or effort. It is copied, used exactly as supplied,
and never selected as a fallback. Its arguments control the goal; Console makes no
claim about the command's effective model or effort.

Initial `session.started`, `account.login` and `workspace.init` event payloads include
`command`, with `mode`, `goal_source`, `model` and `effort`. Settings have `source`
(`explicit`, `cli_default` or custom `unknown`) and an optional explicit `value`.
`cli_default` means the CLI inherits its own configuration, whose effective value
Console does not know. `goal_source` is `prompt`, `none` or `custom_argv`; custom
commands use mode `custom`. Exact argv is returned and persisted in `Session.Argv`,
independently of the existing event retention policy. No new session storage fields
are implied by the event report.

Init keeps existing account selection/reuse and accountless Codex/Claude fallback
behavior; this contract does not qualify fallback authentication or add account
selection. It removes the unqualified Cursor fallback. There is no automatic shell
fallback, headless mode, permission/sandbox override or native spawn retry.
