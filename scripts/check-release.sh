#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
[ "$(id -u)" -ne 0 ] || { echo 'Run release checks as a non-root user.' >&2; exit 1; }
case "$(uname -s)" in Darwin|Linux) ;; *) echo 'Only native macOS/Linux source checks are configured.' >&2; exit 1 ;; esac
export GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS=-mod=readonly
export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE=
export GOCACHE="${GOCACHE:-$PWD/.batuta/runs/console-workflow-versioning/go-cache}"
sh scripts/check-tooling.sh
scanner=$(command -v govulncheck)
go version -m "$scanner" | awk '$1 == "mod" && $2 == "golang.org/x/vuln" && $3 == "v1.8.0" { found=1 } END { exit !found }'
node scripts/check-release-config.mjs
node --test scripts/release-contract.test.mjs
sh scripts/check-assets.sh
npm --prefix web ci
npm --prefix web run test:run
(cd web && ./node_modules/.bin/tsc --noEmit)
npm --prefix web run build
sh scripts/check-assets.sh
go mod verify
go test ./...
go vet ./...
go test -race ./...
mkdir -p .batuta/runs/console-workflow-versioning/bin
go build -o .batuta/runs/console-workflow-versioning/bin/console ./cmd/console
go build -o .batuta/runs/console-workflow-versioning/bin/consoled ./cmd/consoled
govulncheck ./...
echo 'Release source checks passed; product/native/stable qualification remains pending.'
