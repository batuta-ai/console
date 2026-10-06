#!/bin/sh
set -eu
if [ -n "$(git status --porcelain --untracked-files=all -- internal/webui/dist)" ]; then
    git status --short --untracked-files=all -- internal/webui/dist >&2
    echo 'Embedded web assets differ from HEAD; regenerate and commit them.' >&2
    exit 1
fi
