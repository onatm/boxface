#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
export WORK="${WORK:-/var/tmp/boxface-gate-c}"
export PROFILE_FLAKE="$here/profile"
export TOOLS="claude codex opencode pi mise node npm rustc cargo cc git tmux chezmoi"
exec "$here/../gate-b/01-profile.sh"
