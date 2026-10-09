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

IMAGE_TREES="$personal $here/image" exec "$here/../gate-b/02-image.sh"
