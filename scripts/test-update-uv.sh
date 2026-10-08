#!/usr/bin/env bash

set -euo pipefail

ROOT=$(unset CDPATH; cd "$(dirname "$0")/.." && pwd)
for name in PAIRMUX_TEST_UV PAIRMUX_TEST_PYTHON; do
    if [ -z "${!name:-}" ]; then
        printf '%s must name an installed executable for the offline uv tests\n' "$name" >&2
        exit 1
    fi
done

# go test succeeds when a selector matches nothing. Verify the real tagged
# suite exists, rather than accidentally running an ordinary mock-only test.
tests=$(go -C "$ROOT" test -tags uvintegration ./internal/cli -list '^TestUpdateUVIntegration$')
if ! grep -Fxq 'TestUpdateUVIntegration' <<<"$tests"; then
    printf 'TestUpdateUVIntegration is missing; refusing an empty updater gate\n%s\n' "$tests" >&2
    exit 1
fi

exec go -C "$ROOT" test -race -tags uvintegration -count=1 \
    -run '^TestUpdateUVIntegration$' -v ./internal/cli
