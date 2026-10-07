#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
[ "${1:-}" = --fixtures ] && [ "$#" = 1 ] || { echo 'Usage: qualify-terminal.sh --fixtures' >&2; exit 2; }
case "$(uname -s)" in Darwin|Linux) ;; *) echo 'These terminal fixtures require native macOS or Linux.' >&2; exit 2 ;; esac
export GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS=-mod=readonly
# All fixture accounts, stores, control pipes and child homes are disposable.
# No provider CLI, login or personal credentials are used by these Go tests.
names=$(go test ./internal/tui -list '^TestTerminalNative')
for required in TestTerminalNativeRawInput TestTerminalNativeCanonicalSignalsEOF TestTerminalNativeOwnershipResizeDetach TestTerminalNativeExplicitStop TestTerminalNativeActorMaximumHeap TestTerminalNativeFragmentedPromptFinalGlyph TestTerminalNativeUnsupportedGeometryAndRecovery; do
    printf '%s\n' "$names" | grep -Fx "$required" >/dev/null || { echo "Missing required fixture: $required" >&2; exit 1; }
done
go test ./internal/tui -count=1 -timeout=120s -v -run '^Test(Terminal(Native|MaximumControlFloodCancellation|GraphemeLimit|OuterSignalsRestoreHost|Control|Ownership|UnknownTake|PollBeforeTake|ReadonlyDetach|PanelQuit|QuitCancels|StaleGeneration|ResizeStorm|InputBudget|KeyboardModes|View|HoldLimit|ChildGlyphs|ActorTurn|Observer|ControlString)|RootTerminalOwnership)'
go test ./internal/terminalengine ./internal/client ./internal/api ./internal/session -count=1 -timeout=120s -v -run 'Terminal|Attachment'
printf '%s\n' 'Native terminal fixtures passed; provider and Windows qualification remain separate.'
