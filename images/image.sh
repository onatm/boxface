#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
export WORK="${WORK:-/var/tmp/boxface-images/main}"
kernel_out="${KERNEL_WORK:-/var/tmp/boxface-images/kernel}/out"

[ -d "$WORK/profile-tree" ] || { echo "run profile.sh first" >&2; exit 1; }
mkdir -p "$WORK/out"
cp "$kernel_out/vmlinux" "$WORK/out/vmlinux"

personal="$WORK/personal-tree"
rm -rf "$personal"
mkdir -p "$personal/usr/share/boxface"
cp -a "$here/dotfiles" "$personal/usr/share/boxface/dotfiles"
revision="$(cd "$here/dotfiles" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"
echo "$revision" > "$personal/usr/share/boxface/dotfiles-revision"
echo "dotfiles revision: $revision"

run_tree="$WORK/run-tree"
boxface_run="${BOXFACE_RUN:-$here/../bin/linux-amd64/boxface-run}"
[ -x "$boxface_run" ] || { echo "build boxface-run first (task build), or set BOXFACE_RUN" >&2; exit 1; }
rm -rf "$run_tree"
install -D -m 0755 "$boxface_run" "$run_tree/usr/local/bin/boxface-run"

EXTRA_TREES="$WORK/profile-tree $here/guest $personal $run_tree" exec "$here/rootfs.sh"
