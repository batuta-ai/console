#!/bin/sh
set -eu
export GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS=-mod=readonly
[ "$(go env GOVERSION)" = go1.27.2 ] || { echo 'Go 1.27.2 required' >&2; exit 1; }
[ "$(go tool compile -V)" = "compile version go1.27.2" ] || { echo 'Go compiler does not match Go 1.27.2; check GOROOT.' >&2; exit 1; }
[ "$(node --version)" = v26.10.0 ] || { echo 'Node 26.10.0 required' >&2; exit 1; }
[ "$(npm --version)" = 12.2.0 ] || { echo 'npm 12.2.0 required' >&2; exit 1; }
