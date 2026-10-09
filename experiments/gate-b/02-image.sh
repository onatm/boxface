#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
export WORK="${WORK:-/var/tmp/boxface-gate-b}"
gate_a_out="${GATE_A_WORK:-/var/tmp/boxface-gate-a}/out"

[ -d "$WORK/profile-tree" ] || { echo "run 01-profile.sh first" >&2; exit 1; }
mkdir -p "$WORK/out"
cp "$gate_a_out/vmlinux" "$WORK/out/vmlinux"

EXTRA_TREES="$WORK/profile-tree $here/image ${IMAGE_TREES:-}" "$here/../gate-a/02-image.sh"
