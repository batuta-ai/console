# Version diagnostics and release qualification

Run `console --version` or `consoled --version`. Each prints one JSON object
and exits before resolving runtime paths, creating a home, reading credentials,
connecting to a daemon, or starting providers or PTYs. `--home` and `--listen`
may accompany the diagnostic but are not resolved. No host Batuta executable or
plugin is inspected. The two binaries can link different modules.

## Field authorities

The diagnostic adds `binary` (`console` or `consoled`). The authenticated
`GET /v0/capabilities` response adds the same metadata under `provenance`, describing
the serving daemon. Existing Task26 keys and their meanings remain unchanged:
`runtime.build.version` is the observed Go main module version, `revision` is
observed VCS metadata, and `modified` is nullable observed VCS dirty state.

- `go_version`: actual `runtime.Version()` toolchain, with `go_authority`
  `runtime.Version`; neither the go.mod directive nor a tool on PATH.
- `release.version`, `release.revision`: optional linker labels. Unset values
  are `development` and `unknown`; `authority` is `unset` unless either field
  is supplied, when it is `linker_injected`. Partial injection retains the
  explicit fallback for the other field. Labels never replace observations.
- `observed.version`, `observed.revision`, `observed.modified`: embedded
  `debug.ReadBuildInfo` main module/VCS data, authority `go_build_info`.
  Missing version/revision is `unknown`; missing modified state is null.
  Native Git builds may report a VCS pseudo-version (including `+dirty`);
  archive builds may report `(devel)`. These values are preserved verbatim.
- `core`: only the actual linked `github.com/batuta-ai/core` dependency from
  embedded Go build information. `status` is `linked`, `replaced`, or `missing`.
  `version` is the original module version, or `development` for empty/devel
  linked versions, or `unknown` when absent. `replacement_version` is `none`
  without a replacement, otherwise its version or `development`. No replacement
  paths are exposed. A replaced module is not proof of the original published
  module's implementation. Authority is `go_build_info`; this is not registry
  verification, installed-tool discovery, or semantic compatibility proof.
- `plugin`: status/version/replacement version are `unknown`, authority
  `no_trusted_manifest`. No authoritative host/plugin manifest is available in
  this scope. The local limits package does not establish a host plugin version.
- `compatibility.console` and `compatibility.api`: source-declared product
  `batuta-console` and API surface `v0`. They identify the served contract;
  they are not a supported version range.
- `compatibility.core`: `unverified`, authority `no_qualified_contract`.
  `compatibility.plugin`: `unknown`, authority `no_trusted_manifest`.
- `compatibility.stable`: `pending`, authority `release_qualification_required`.
  `compatibility.native`: `pending`, authority
  `macos_linux_qualification_required`. These are explicit qualification
  requirements, not dynamically inferred successes.

The query is bounded to embedded build data and static declarations. It does
not promote PATH installation, account/model readiness or provider banners to
verified compatibility. Existing provider/PTY observations retain their own
separate authorities and guards.

## Independent beta numbering and bootstrap

Console uses the accepted `v0.1.0-beta.N` sequence, independent of the linked
Core version.
`.release-please-manifest.json` starts at `0.1.0-beta.0` as **unpublished
bookkeeping**, not an existing release. The first published release will be
`v0.1.0-beta.1`. The repository is public at [batuta-ai/console](https://github.com/batuta-ai/console); no Console
release has been published. Repository creation and source CI do not publish a tag.

The bootstrap boundary is commit `3485c1ca1f02f70ba0101422ee41d4d6fafb2ddb`.
Release-please considers subsequent Conventional Commits; older history is
intentionally excluded from the initial changelog. Keep that boundary in the
repository history. The `go` strategy has no version file: it updates the
manifest and `docs/CHANGELOG.md`, not Core dependencies or runtime metadata.
The prerelease strategy increments beta versions. The vendored schema in
`scripts/release-please-schema.json` is from release-please v17.11.2, commit
`05c6a4f71022304d4edad24ea90c1c16324503d5`, at
[the official schema](https://raw.githubusercontent.com/googleapis/release-please/05c6a4f71022304d4edad24ea90c1c16324503d5/schemas/config.json).

## Frozen local commands

Put these exact installed tools on PATH: Go **1.27.2**, Node **26.10.0**, npm
**12.2.0**, govulncheck **1.8.0**, GoReleaser **2.18.2**, actionlint **1.7.12**.
The preparation reused isolated tools; it did not update personal installations.
`.node-version` and `web/package.json#packageManager` record the web tooling.
Git must support `git show-ref --exists`; unsupported versions fail the shared
tooling check before source tests run.
No web library graph or `go.mod`/`go.sum` upgrade is part of this work.
If the shell exports an older `GOROOT`, unset it before using the selected SDK;
the preflight verifies both the Go command and compiler versions.

From the checkout root as a non-root user:

```sh
sh scripts/check-release.sh
sh scripts/package-release.sh
# Equivalent convenience targets: make check-release; make package-release
node scripts/check-release-config.mjs
node --test scripts/release-contract.test.mjs
actionlint
goreleaser check
```

The shared check performs frozen-tool validation, schema/policy and behavioral
checks, `npm ci`, web tests, TypeScript checking, web build and asset drift checks,
module verification, Go tests/vet/race, both command builds and source govulncheck.
It stops at the first failure. Assets must match HEAD, including staged and
untracked files; regenerate through Vite and commit deliberate changes before
qualification. Never edit `internal/webui/dist` manually. The check uses an owned
`.batuta/runs/console-workflow-versioning/go-cache` unless GOCACHE is supplied.
Uncached dependencies and the vulnerability database require network reads;
access failures are failures, not a clean scan. No tests are skipped to package
an actual release.

The default package command rebuilds the web bundle and checks drift, then
creates five local snapshot archives under `dist/`: four tar.gz files for
darwin/linux × amd64/arm64 and one ZIP for Windows amd64, all with CGO disabled.
Each tar.gz contains `console` and `consoled`; the Windows ZIP contains
`console.exe` and `consoled.exe`. Every archive also contains the exact LICENSE,
README.md and THIRD_PARTY_NOTICES files, including the copied MIT notice.
`checksums.txt` contains one SHA256 digest for each of the five archives.
Local packaging requires `tar`, `zip` and `unzip` (including `unzip -Z1`). After
verifying the packages, both snapshot and release commands automatically run
`scripts/package-contract.test.mjs` against temporary copies of those real
archives. It checks the unmodified packages and rejects wrong OS/architecture
binaries, a missing Windows executable, an altered MIT notice and an incorrect
digest. The copies are removed; the distribution files remain unchanged. This
artifact/build-metadata proof does **not** establish the full source-check
result or native Windows runtime/provider qualification. Both binaries receive
the same full HEAD revision and a
`0.1.0-beta.N-snapshot.<full-revision>` label. Dirty source is permitted only for
snapshots and remains visible in observed VCS metadata. GoReleaser publishing is
disabled, including for actual-release packaging.

For an already existing, reviewed beta tag, the future release job runs:

```sh
sh scripts/package-release.sh --release v0.1.0-beta.1
```

This illustrative command requires that exact existing tag to match the manifest
and HEAD, a clean source tree, and the entire shared check. It then builds with
the actual `v`-prefixed tag and full revision labels. Stable tags, v1 tags,
malformed tags, manifest mismatches and the bootstrap seed fail before packaging.
There is no stable override. `scripts/verify-packages.mjs` verifies archive count,
contents, documentation bytes, checksums and both binaries' embedded build data.
Only `consoled` currently links Core; `console` may truthfully report it missing.

## Repository setup and reviewed release flow

The public repository is `batuta-ai/console`, its default branch is `main`, and
Actions is enabled. The bootstrap history has been retained.
All owner/repository references come from GitHub context; no destination or
visibility is hard-coded. Protect the default branch and require both native
macOS/Linux `Console checks` jobs plus ordinary review for release PRs.

The organization already supplies `RELEASE_PLEASE_TOKEN` to this repository.
For another installation, configure a secret named `RELEASE_PLEASE_TOKEN` using an owner-approved
GitHub App installation token or fine-grained token with repository Contents,
Issues and Pull requests write permissions, matching the pinned release-please
action's documented requirements and the proposal job. It lets release-please
open/update PRs and create the prerelease/tag after review and merge. A token that triggers PR CI is
needed: the default GITHUB_TOKEN does not start workflows from its own PR events.
Credentials are not provisioned or stored by this preparation. All other jobs
have read-only contents permission except the narrowly scoped package upload job.
No workflow auto-merges or uses admin merge privileges.

`ci.yml` runs the shared command on native `macos-15` and `ubuntu-24.04` hosted
runners without sudo/root. `release.yml` acts only on the repository's default
branch. After checks pass, release-please opens/updates a version/changelog PR.
The PR receives normal review and checks. After its merge, release-please creates
the beta tag and GitHub prerelease. The same workflow consumes its outputs, so
it does not depend on a token-triggered tag workflow. It checks the exact tagged
source on both OS runners, then the package job validates the beta guard and
runs the shared check again before packaging. Upload uses the current repository
and only an existing, non-draft prerelease whose tag matches. The authorized
upload includes exactly six assets: the four Mac/Linux tar.gz archives, the
Windows amd64 ZIP and checksums.txt. Failed jobs leave packages unpublished;
an already created prerelease can remain without assets.
Recovery requires reviewing the failure and explicitly packaging/uploading the
same verified tag (using the scoped Node `scripts/upload-release.mjs` entry point
only when separately authorized); no automatic overwrite or stable escape is configured.

Every external action is pinned to its resolved full commit, annotated with its
selected release version. Runner OS images are hosted rolling images; this is
frozen language/check tooling, not a claim of bit-for-bit reproducible OS images.
Normal review must assess any future changes to workflow permissions or guards.

## Local evidence and qualification limits — 2026-10-06

Worker evidence is local and ignored under
`.batuta/runs/console-workflow-versioning/`; it is not a public CI result.
See `receipt.md` and `commands.md` there for exact commands, outputs and limits.
The contract's red/green tests cover tag rejection and tracked/staged/untracked
asset drift. `schema.log`, `actionlint.log` and `archives-green.log` record the
corresponding local checks. Snapshots retain the actual source revision and
observed dirty state; they are not published versions.

The historical worker runs stopped the shared check at Go HTTP tests because
the worker sandbox forbade local socket binds; packaged daemon startup failed at
the same boundary. A separate source govulncheck attempt could not resolve
`vuln.go.dev`, and Go emitted denied shared module stat-cache write warnings.
Those failed runs remain recorded in `check-release-native-mac.log`,
`govulncheck-source.log` and `native-proof.log` in the worker evidence directory;
they did not establish full Go/race, a clean scan or daemon HTTP proof.

The independent conductor follow-up on native macOS arm64 on **2026-10-06**
passed the full shared check: normal Go tests, race tests, vet, both binary
builds, npm ci, all 43 web tests, TypeScript checking, web build without asset
drift, module verification and source govulncheck with no vulnerabilities found.
Copies of the actual proofs are under `.batuta/runs/conductor-verification/`:
`verification-status.json` records exit 0 for the checks and packaging, and
`verify-check-release.log` contains the shared-check output. These proofs apply
to candidate `ae1571c7669474ca2007213252aa46b2f87a7f28`, tree
`e35df6c217abadec2363486ceb52b8ddd3a8e10e`, before this permission/documentation
correction; they are not new-head runs.

`verify-package-release.log` records four verified Mac/Linux snapshot archives,
including checksums, contents and both binaries' embedded build records.
`native-package-proof.json` records both packaged macOS arm64 diagnostics and
exact HTTP index/JS/CSS asset checks; the native follow-up also passed graceful
daemon shutdown. Other packaged targets have cross-build/metadata proof only.
The conductor will rerun targeted checks and packaging at the corrected HEAD,
then independent read-only review. The earlier reviewer's denied reads of
proofs outside its working directory were a host-access issue, not a failed
source check. No completed Sonnet review or successful GitHub run is claimed.

At that qualification, the graph pinned **Core v1.1.0-beta.49** without replacement and Lyra
React/styles **1.1.0**. The public approved-plan snapshot API was available to
the next Task21; Console
did not yet use native `loop.New`/`loop.Resume` execution.
Linker labels never replace actual toolchain, VCS, dependency or compatibility
authorities. Existing `compatibility.native` and `compatibility.stable` remain
pending, and the historical native authority string still names macOS/Linux.

The intended product scope includes Codex and Claude on macOS, Linux and native
Windows. Current Task42 build portability is complete, while native Windows
Task43 runtime (ConPTY/protection) and Task44 provider QA remain pending; WSL
proves Linux only. macOS/Linux full provider, browser and interactive TUI operation,
semantic Core/plugin compatibility, Core official CLI binary security, independent
review and Task37's final full-matrix/native distribution and stable release gates
remain open. Cross-built Linux/Windows artifacts cannot close runtime gates.
No stable release, complete native support or successful publication is claimed.

## Current Core beta55 adoption guidance — 2026-10-15

Current dependency policy and runtime expectations supersede earlier historical pins.
The active repository graph pins [Core v1.1.0-beta.55](https://github.com/batuta-ai/core/releases/tag/v1.1.0-beta.55) without replacement.
On macOS and Linux, the Core CLI must be exactly `v1.1.0-beta.55` with the required
capabilities. `consoled -core /absolute/path/to/batuta` selects a private explicit
executable; otherwise `batuta` on PATH is used. Windows uses the Core SDK included
with the Console image, with strict version, public module checksum and no-replacement
checks, and needs no external Core CLI. Native Windows qualification remains open.
Earlier native package evidence does not qualify beta55.
For full details on the Core beta55 adoption, verified checksums, Core CI provenance limits, local bounded checks, and failed Console CI run [38754176954](https://github.com/batuta-ai/console/actions/runs/38754176954), see the [Published Core beta55 adoption and native runtime qualification — 2026-10-15 section in dependency-policy.md](dependency-policy.md#published-core-beta55-adoption-and-native-runtime-qualification--2026-10-15).
