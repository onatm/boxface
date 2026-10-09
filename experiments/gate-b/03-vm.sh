#!/usr/bin/env bash
set -euo pipefail

export WORK="${WORK:-/var/tmp/boxface-gate-b}"
exec "$(dirname "$0")/../gate-a/03-vm.sh" "$@"
