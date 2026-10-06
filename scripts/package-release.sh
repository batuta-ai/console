#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
mode=snapshot
case "$#:$*" in
    0:) ;;
    2:--release\ *) mode=release; node scripts/verify-beta.mjs "$2" ;;
    *) echo 'Usage: sh scripts/package-release.sh [--release v0.x.y-beta.N]' >&2; exit 1 ;;
esac
export GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS=-mod=readonly
export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE=
export GOCACHE="${GOCACHE:-$PWD/.batuta/runs/console-workflow-versioning/go-cache}"
sh scripts/check-tooling.sh
node scripts/check-release-config.mjs
goreleaser --version | awk '$1 == "GitVersion:" && $2 == "2.18.2" { found=1 } END { exit !found }'
export CONSOLE_REVISION="$(git rev-parse HEAD)"
if [ "$mode" = release ]; then
    [ "$(git rev-parse "$2^{commit}")" = "$CONSOLE_REVISION" ] || { echo 'Release tag must identify HEAD.' >&2; exit 1; }
    [ -z "$(git status --porcelain --untracked-files=all)" ] || { echo 'Release requires a clean source tree.' >&2; exit 1; }
    export CONSOLE_VERSION="$2"
    sh scripts/check-release.sh
    [ -z "$(git status --porcelain --untracked-files=all)" ] || { echo 'Checks changed release source.' >&2; exit 1; }
else
    export CONSOLE_VERSION="$(node -p "JSON.parse(require('fs').readFileSync('.release-please-manifest.json'))['.']")-snapshot.$CONSOLE_REVISION"
    npm --prefix web ci
    npm --prefix web run build
    sh scripts/check-assets.sh
fi
goreleaser check
if [ "$mode" = release ]; then
    GORELEASER_CURRENT_TAG="$CONSOLE_VERSION" goreleaser release --clean --parallelism 1 --skip=publish
else
    goreleaser release --snapshot --clean --parallelism 1
fi
node scripts/verify-packages.mjs dist "$CONSOLE_VERSION" "$CONSOLE_REVISION"
echo "Local $mode packages verified in dist/; nothing uploaded."
