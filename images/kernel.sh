#!/usr/bin/env bash
set -euo pipefail

KERNEL_VERSION=6.18.55
KERNEL_SHA256=f410638061a165c12f42ab871d2f3fcd525515359b5faeee80969cff84524df9
FC_VERSION=v1.17.0
FC_CONFIG=microvm-kernel-ci-x86_64-6.18.config
MOBY_COMMIT=417af1ddca4ae495505b653f3be9037842470035

here="$(cd "$(dirname "$0")" && pwd)"
work="${WORK:-/var/tmp/boxface-images/kernel}"
out="$work/out"
src="$work/linux-${KERNEL_VERSION}"
mkdir -p "$work" "$out"

apt-get update
apt-get install -y build-essential bc bison flex libelf-dev libssl-dev xz-utils curl

tarball="$work/linux-${KERNEL_VERSION}.tar.xz"
[ -f "$tarball" ] || curl -fsSL -o "$tarball" "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz"
echo "${KERNEL_SHA256}  $tarball" | sha256sum -c -
rm -rf "$src"
tar -xJf "$tarball" -C "$work"

curl -fsSL -o "$work/base.config" "https://raw.githubusercontent.com/firecracker-microvm/firecracker/${FC_VERSION}/resources/guest_configs/${FC_CONFIG}"
cd "$src"
scripts/kconfig/merge_config.sh -m "$work/base.config" "$here/kernel.fragment"
make olddefconfig

missing=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  grep -qxF "$line" .config || { echo "dropped by olddefconfig: $line" >&2; missing=1; }
done < "$here/kernel.fragment"
[ "$missing" -eq 0 ]

start=$(date +%s)
make -j"$(nproc)" vmlinux
end=$(date +%s)

cp vmlinux "$out/vmlinux"
cp .config "$out/kernel.config"
diff -u "$work/base.config" .config > "$out/kernel.config.diff" || true

curl -fsSL -o "$work/check-config.sh" "https://raw.githubusercontent.com/moby/moby/${MOBY_COMMIT}/contrib/check-config.sh"
sh "$work/check-config.sh" "$out/kernel.config" 2>&1 | sed 's/\x1b\[[0-9;]*m//g' > "$out/check-config.txt" || true
if awk '/^Generally Necessary:/{s=1} /^Optional Features:/{s=0} s' "$out/check-config.txt" | grep -q ': missing'; then
  grep ': missing' "$out/check-config.txt" >&2
  echo "required Docker kernel options missing" >&2
  exit 1
fi

{
  echo "kernel: ${KERNEL_VERSION}"
  echo "base config: firecracker ${FC_VERSION} ${FC_CONFIG}"
  echo "build seconds: $((end - start)) on $(nproc) vcpus"
  echo "vmlinux bytes: $(stat -c %s "$out/vmlinux")"
  echo "vmlinux sha256: $(sha256sum "$out/vmlinux" | cut -d' ' -f1)"
} | tee "$out/kernel-info.txt"
