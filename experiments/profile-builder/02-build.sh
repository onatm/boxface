#!/usr/bin/env bash
# Builds a profile flake inside a Firecracker builder VM into $WORK/out/profile.img. Run as root on the host after
# 01-images.sh, with boxface-worker running so its nftables rules isolate the bfx tap.
#   02-build.sh [FLAKE_DIR [DOTFILES_DIR]]   defaults: experiments/gate-c/profile and experiments/gate-c/dotfiles
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
work="${WORK:-/var/tmp/boxface-profile-spike}"
out="$work/out"
flake="${1:-$repo/experiments/gate-c/profile}"
dotfiles="${2:-$repo/experiments/gate-c/dotfiles}"
tap=bfxprofspike
memory_mib="${MEMORY_MIB:-3072}"
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

inputs="$work/inputs"
rm -rf "$inputs"
mkdir -p "$inputs/profile"
cp "$flake/flake.nix" "$flake/flake.lock" "$inputs/profile/"
cp -a "$dotfiles" "$inputs/dotfiles"
(cd "$dotfiles" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1) > "$inputs/dotfiles-revision"
rm -f "$work/inputs.ext4"
truncate -s 64M "$work/inputs.ext4"
mkfs.ext4 -q -L boxface-inputs -d "$inputs" "$work/inputs.ext4"

[ -f "$work/cache.img" ] || truncate -s 40G "$work/cache.img"
rm -f "$out/profile.img"
truncate -s 16G "$out/profile.img"

ip link show "$tap" > /dev/null 2>&1 || ip tuntap add dev "$tap" mode tap
ip addr replace 172.20.250.1/30 dev "$tap"
ip link set "$tap" up

api="$work/builder.sock"
rm -f "$api"
cat > "$work/builder.json" <<JSON
{
  "boot-source": {
    "kernel_image_path": "$out/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off net.ifnames=0 ip=172.20.250.2::172.20.250.1:255.255.255.252::eth0:off"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$out/builder.ext4", "is_root_device": true, "is_read_only": true},
    {"drive_id": "cache", "path_on_host": "$work/cache.img", "is_root_device": false, "is_read_only": false},
    {"drive_id": "inputs", "path_on_host": "$work/inputs.ext4", "is_root_device": false, "is_read_only": true},
    {"drive_id": "output", "path_on_host": "$out/profile.img", "is_root_device": false, "is_read_only": false}
  ],
  "machine-config": {"vcpu_count": 2, "mem_size_mib": $memory_mib},
  "network-interfaces": [{"iface_id": "eth0", "guest_mac": "06:00:ac:14:fa:02", "host_dev_name": "$tap"}]
}
JSON

started=$(date +%s)
firecracker --api-sock "$api" --config-file "$work/builder.json" > "$work/builder-console.log" 2>&1 &
pid=$!
peak_kib=0
while kill -0 "$pid" 2> /dev/null; do
  rss=$(awk '/VmHWM/ {print $2}' "/proc/$pid/status" 2> /dev/null || echo 0)
  [ "${rss:-0}" -gt "$peak_kib" ] && peak_kib=$rss
  if [ $(( $(date +%s) - started )) -gt 3600 ]; then kill "$pid"; echo "build timed out" >&2; fi
  sleep 2
done
wait "$pid" || true
elapsed=$(( $(date +%s) - started ))
ip link del dev "$tap"

result="$(grep -a 'BOXFACE-RESULT' "$work/builder-console.log" | tail -1 || true)"
echo "${result:-BOXFACE-RESULT status=missing}"
echo "VM wall time: ${elapsed} s; firecracker peak RSS: $(( peak_kib / 1024 )) MiB of ${memory_mib} MiB"
echo "cache disk used: $(( $(du -k "$work/cache.img" | cut -f1) / 1024 )) MiB; profile drive used: $(( $(du -k "$out/profile.img" | cut -f1) / 1024 )) MiB"
case "$result" in *status=ok*) ;; *) grep -a -v '^\[' "$work/builder-console.log" | tail -40; exit 1 ;; esac
