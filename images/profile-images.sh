#!/usr/bin/env bash
# Builds the profile builder image (Debian + static Nix) and a base image with no profile. Run as root on the host
# after kernel.sh and with Nix installed (profile.sh installs it).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
work="${WORK:-/var/tmp/boxface-images/profiles}"
out="$work/out"
kernel="${KERNEL_WORK:-/var/tmp/boxface-images/kernel}"
mkdir -p "$out"
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
[ -x "$repo/bin/linux-amd64/boxface-run" ] || { echo "build boxface-run first (task build)" >&2; exit 1; }
export PATH="/nix/var/nix/profiles/default/bin:$PATH"
export NIX_CONFIG="experimental-features = nix-command flakes"

nix build --inputs-from "$here/profile" nixpkgs#nixStatic -o "$work/nix-static"

started=$(date +%s)
mkosi="${MKOSI:-${MKOSI_DIR:-/var/tmp/boxface-images/mkosi}/bin/mkosi}"
[ -x "$mkosi" ] || { echo "no mkosi checkout found; run rootfs.sh or image.sh once or set MKOSI" >&2; exit 1; }
"$mkosi" -C "$here/builder" --output-directory="$work/builder" -f build
tree="$work/builder/rootfs"
install -d "$tree/usr/local/bin"
install -m 0755 "$(readlink -f "$work/nix-static/bin/nix")" "$tree/usr/local/bin/nix"
for name in $(cd "$work/nix-static/bin" && ls | grep -vx nix); do ln -sfn nix "$tree/usr/local/bin/$name"; done
size_mb=$(( $(du -sm "$tree" | cut -f1) * 12 / 10 + 128 ))
rm -f "$out/builder.ext4"
truncate -s "${size_mb}M" "$out/builder.ext4"
mkfs.ext4 -q -L boxface-builder -d "$tree" "$out/builder.ext4"
echo "builder image: ${size_mb} MiB in $(( $(date +%s) - started )) s; $("$tree/usr/local/bin/nix" --version)"

run_tree="$work/run-tree"
rm -rf "$run_tree"
install -D -m 0755 "$repo/bin/linux-amd64/boxface-run" "$run_tree/usr/local/bin/boxface-run"
started=$(date +%s)
WORK="$work/base" EXTRA_TREES="$here/guest $run_tree $here/base" \
  "$here/rootfs.sh" > "$work/base-build.log" 2>&1 || { tail -20 "$work/base-build.log"; exit 1; }
cp "$work/base/out/rootfs.ext4" "$out/base.ext4"
cp "$kernel/out/vmlinux" "$out/vmlinux"
echo "base image: $(( $(stat -c %s "$out/base.ext4") >> 20 )) MiB in $(( $(date +%s) - started )) s"
