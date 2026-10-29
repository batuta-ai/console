# Routing — Batuta Console

<!-- inputs: profile.md@sha256:5d6f540d4ec1 -->

| Lane | Domain | Executor | Model | Effort |
| --- | --- | --- | --- | --- |
| low | * | opencode | opencode/glm-5.3-flash | default |
| low | docs | agy | gemini-3.8-flash-low | default |
| medium | * | codex | gpt-6.1-sol | default |
| high | * | codex | gpt-6-astra | high |
| critical | * | self | session | session |

| Role | Lane | Executor | Model |
| --- | --- | --- | --- |
| research | low | opencode | opencode/ling-3.1-flash-free |
| research | medium | codex | gpt-6.1-sol |
| review | — | codex | gpt-6-astra |

Review policy updated on 2026-10-28 at the maintainer's request: all project reviews, including deep and final reviews, use Codex with `gpt-6-astra`. This supersedes earlier Claude/Opus review requirements in conducting plans and proposals. Keep historical review evidence unchanged. Reviews remain independent and read-only; do not fall back to Claude unless the maintainer changes this policy. The model ID was confirmed with `codex debug models --bundled`, and Codex is logged in through ChatGPT.

Native model IDs verified in batuta inventory on 2026-10-03. Installed and used: opencode, agy, codex, claude. cursor-agent is missing and is not routed. The inventory also reports compozy with no resolved model set; it is outside the shipped skill adapters and is not proposed. CLI availability/model listing does not qualify product account isolation or authentication.

The Effort column records the conductor's invocation choice; the core table parser reads executor/model and does not enforce this extra column. Freeze and pass high explicitly when invoking high-lane Codex through its adapter. self means the current conducting session, not a CLI or a guessed model slug.

For scope containing hidden directories, do not use agy; route through codex or the conductor as required by the skill. Unavailable executor or two failed verifications escalates one lane with an announcement. Never silently replace a model. Research uses its own ladder; two low failures move to medium, then top-lane failure permits a self sweep. Review is read-only.

The project-map sweep on 2026-10-03 used a clean, isolated snapshot because the audit docs in the original checkout are untracked. Two low research attempts timed out without a complete report, then the top configured research lane also timed out with a clean guard. The conductor verified the map from source. These probe failures do not imply the CLIs are uninstalled; recheck readiness before actual work.
