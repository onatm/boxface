#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
export WORK="${WORK:-/var/tmp/boxface-gate-c}"
personal="$WORK/personal-tree"

rm -rf "$personal"
mkdir -p "$personal/usr/share/boxface"
cp -a "$here/dotfiles" "$personal/usr/share/boxface/dotfiles"
revision="$(cd "$here/dotfiles" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"
echo "$revision" > "$personal/usr/share/boxface/dotfiles-revision"
echo "dotfiles revision: $revision"

run_tree="$WORK/run-tree"
boxface_run="${BOXFACE_RUN:-$here/../../bin/linux-amd64/boxface-run}"
[ -x "$boxface_run" ] || { echo "build boxface-run first (task build), or set BOXFACE_RUN" >&2; exit 1; }
rm -rf "$run_tree"
install -D -m 0755 "$boxface_run" "$run_tree/usr/local/bin/boxface-run"

IMAGE_TREES="$personal $here/image $run_tree" exec "$here/../gate-b/02-image.sh"
