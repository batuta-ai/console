#!/bin/sh
# Check the current candidate, including uncommitted files, without a sibling core.
set -eu

source_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/console-standalone.XXXXXXXX")
trap 'rm -rf -- "$scratch"' 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$scratch/source" "$scratch/build" "$scratch/install"
(
    cd "$source_root"
    # Select root entries explicitly: tar exclusions can also match nested bin/.
    set --
    for entry in ./* ./.[!.]* ./..?*; do
        [ -e "$entry" ] || [ -L "$entry" ] || continue
        case "$entry" in
            ./.git|./.batuta|./node_modules|./bin|./vendor) continue ;;
        esac
        set -- "$@" "$entry"
    done
    tar --exclude=node_modules -cf "$scratch/source.tar" "$@"
)
tar -C "$scratch/source" -xf "$scratch/source.tar"

(
    cd "$scratch/source"
    # Do not inherit workspace or flags that could hide a broken release module.
    export GOWORK=off GOFLAGS=-mod=readonly GOENV=off
    # Public dependencies must keep checksum verification enabled.
    export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE=
    export GOBIN="$scratch/install"
    replacements=$(go list -m -f '{{if .Replace}}{{.Path}} => {{.Replace.Path}}{{end}}' github.com/batuta-ai/core)
    if [ -n "$replacements" ]; then
        printf '%s\n' 'Standalone verification rejects core module replacements:' "$replacements" >&2
        exit 1
    fi
    go mod download
    go mod verify
    go list -m github.com/batuta-ai/core
    go test ./...
    go build -o "$scratch/build/console" ./cmd/console
    go build -o "$scratch/build/consoled" ./cmd/consoled
    go install ./cmd/console ./cmd/consoled
    test -x "$scratch/build/console"
    test -x "$scratch/build/consoled"
    test -x "$scratch/install/console"
    test -x "$scratch/install/consoled"
)
printf '%s\n' 'Standalone source tests, builds and installs passed for both binaries.'
