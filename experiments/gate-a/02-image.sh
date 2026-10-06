#!/usr/bin/env bash
set -euo pipefail

MKOSI_VERSION=v27.1
MKOSI_COMMIT=ecf7f5702bb2052b7a69cab3fe7d37bc3b9ad77b
DOCKER_KEY_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88

here="$(cd "$(dirname "$0")" && pwd)"
work="${WORK:-/var/tmp/boxface-gate-a}"
out="$work/out"
mkdir -p "$work" "$out"

apt-get update
apt-get install -y git python3 debian-archive-keyring gnupg e2fsprogs curl

mkosi_dir="$work/mkosi"
[ -d "$mkosi_dir" ] || git clone --depth 1 --branch "$MKOSI_VERSION" https://github.com/systemd/mkosi "$mkosi_dir"
[ "$(git -C "$mkosi_dir" rev-parse HEAD)" = "$MKOSI_COMMIT" ] || { echo "unexpected mkosi commit" >&2; exit 1; }

sandbox="$work/sandbox"
rm -rf "$sandbox"
mkdir -p "$sandbox/etc/apt/keyrings" "$sandbox/etc/apt/sources.list.d"
curl -fsSL -o "$sandbox/etc/apt/keyrings/docker.asc" https://download.docker.com/linux/debian/gpg
gpg --show-keys --with-colons "$sandbox/etc/apt/keyrings/docker.asc" | awk -F: '$1 == "fpr" {print $10}' | grep -qx "$DOCKER_KEY_FINGERPRINT" \
  || { echo "unexpected Docker signing key" >&2; exit 1; }
cat > "$sandbox/etc/apt/sources.list.d/docker.sources" <<EOF
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: trixie
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
EOF

start=$(date +%s)
"$mkosi_dir/bin/mkosi" -C "$here/image" --sandbox-tree="$sandbox" --output-directory="$work/image" -f build
end=$(date +%s)

tree="$work/image/rootfs"
fail=0
if compgen -G "$tree/etc/ssh/ssh_host_*" > /dev/null; then echo "SSH host keys baked into image" >&2; fail=1; fi
machine_id="$(cat "$tree/etc/machine-id" 2>/dev/null || true)"
case "$machine_id" in ""|uninitialized) ;; *) echo "machine ID baked into image" >&2; fail=1 ;; esac
if [ -e "$tree/usr/bin/sudo" ]; then echo "sudo present in image" >&2; fail=1; fi
if grep -rlIE --'-----BEGIN ([A-Z]+ )?PRIVATE KEY-----' "$tree" 2>/dev/null; then echo "private keys found in image" >&2; fail=1; fi
[ "$fail" -eq 0 ]

used_mb=$(du -sm "$tree" | cut -f1)
size_mb=$((used_mb * 12 / 10 + 256))
rm -f "$out/rootfs.ext4"
truncate -s "${size_mb}M" "$out/rootfs.ext4"
mkfs.ext4 -q -L boxface-root -d "$tree" "$out/rootfs.ext4"
cp "$work/image/rootfs.manifest" "$out/rootfs.manifest.json"

{
  echo "distribution: debian trixie"
  echo "mkosi: ${MKOSI_VERSION} (${MKOSI_COMMIT})"
  echo "build seconds: $((end - start)) on $(nproc) vcpus"
  echo "tree MiB: ${used_mb}"
  echo "rootfs.ext4 MiB: ${size_mb}"
  echo "rootfs.ext4 sha256: $(sha256sum "$out/rootfs.ext4" | cut -d' ' -f1)"
} | tee "$out/image-info.txt"
