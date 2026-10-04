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

## Metadata injection

Use the selected native Go 1.27.1 SDK, not the shell's Go 1.24 default. Build each
binary with the same desired labels, for example:

```sh
export GOTOOLCHAIN=local
/path/to/go1.27.1/bin/go build -ldflags='-X github.com/batuta-ai/console/internal/buildinfo.ReleaseVersion=1.2.3 -X github.com/batuta-ai/console/internal/buildinfo.ReleaseRevision=abcdef' -o bin/console ./cmd/console
/path/to/go1.27.1/bin/go build -ldflags='-X github.com/batuta-ai/console/internal/buildinfo.ReleaseVersion=1.2.3 -X github.com/batuta-ai/console/internal/buildinfo.ReleaseRevision=abcdef' -o bin/consoled ./cmd/consoled
```

The labels above are illustrative, not published versions. Compare each
binary's diagnostic with `go version -m binary`. Injection does not change its
actual toolchain or observed module/VCS metadata. Synthetic labels used in tests
are test evidence only; they create no tag or publication.

## Qualification limits

The selected source graph pins Core `v1.1.0-beta.46` and Lyra React/styles `1.1.0`.
A linked pre-release does not establish stable Core/plugin compatibility.
Stable selection and publication require a trustworthy versioned Core/plugin
contract and exact-source qualification, including native macOS/Linux checks,
independent review, and provider/browser/TUI testing. Draft unpublished Core
PR145 is not compatibility authority. Task19 and Task20's approval-model decision
remain independent. Windows is outside full runtime qualification. Version
labels cannot remove readiness or execution guards or satisfy these requirements.
