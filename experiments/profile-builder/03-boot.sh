#!/usr/bin/env bash
# Boots the base image with the built profile drive at /nix, checks the tools and personalisation over SSH, and stops.
set -euo pipefail

work="${WORK:-/var/tmp/boxface-profile-spike}"
out="$work/out"
tap=bfxprofspike
vsock="$work/boot-vsock.sock"
api="$work/boot.sock"
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

key="$work/spike_key"
[ -f "$key" ] || ssh-keygen -q -t ed25519 -N '' -f "$key"
head -c 4096 /dev/zero > "$work/keys.img"
dd if="$key.pub" of="$work/keys.img" conv=notrunc status=none
rm -f "$work/data.ext4"
truncate -s 4G "$work/data.ext4"
mkfs.ext4 -q -L boxface-data "$work/data.ext4"

ip link show "$tap" > /dev/null 2>&1 || ip tuntap add dev "$tap" mode tap
ip addr replace 172.20.250.1/30 dev "$tap"
ip link set "$tap" up
rm -f "$api" "$vsock" "${vsock}_1024" "$work/ready.txt"
cat > "$work/boot.json" <<JSON
{
  "boot-source": {
    "kernel_image_path": "$out/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off net.ifnames=0 ip=172.20.250.2::172.20.250.1:255.255.255.252::eth0:off"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$out/base.ext4", "is_root_device": true, "is_read_only": true},
    {"drive_id": "data", "path_on_host": "$work/data.ext4", "is_root_device": false, "is_read_only": false},
    {"drive_id": "keys", "path_on_host": "$work/keys.img", "is_root_device": false, "is_read_only": true},
    {"drive_id": "profile", "path_on_host": "$out/profile.img", "is_root_device": false, "is_read_only": true}
  ],
  "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048},
  "network-interfaces": [{"iface_id": "eth0", "guest_mac": "06:00:ac:14:fa:02", "host_dev_name": "$tap"}],
  "vsock": {"guest_cid": 3, "uds_path": "$vsock"}
}
JSON
socat -u "UNIX-LISTEN:${vsock}_1024" "OPEN:$work/ready.txt,creat,trunc" &
started=$(date +%s.%N)
firecracker --api-sock "$api" --config-file "$work/boot.json" > "$work/boot-console.log" 2>&1 &
pid=$!
for _ in $(seq 1 120); do
  grep -qx ready "$work/ready.txt" 2> /dev/null && break
  kill -0 "$pid" 2> /dev/null || { tail -30 "$work/boot-console.log"; exit 1; }
  sleep 1
done
echo "ready after $(echo "$(date +%s.%N) - $started" | bc) s"

ssh -i "$key" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR dev@172.20.250.2 bash -l <<'GUEST' || true
findmnt -no SOURCE,OPTIONS /nix
echo "profile: $(readlink -f /opt/boxface/profile)"
for t in claude codex opencode pi mise node git tmux chezmoi; do printf '%-9s %s\n' "$t" "$(command -v $t > /dev/null && $t --version 2>&1 | head -1 || echo MISSING)"; done
for _ in $(seq 1 20); do [ -f ~/.local/state/boxface/personalisation ] && break; sleep 1; done
sed -n '1,2p;/managed:/,$p' ~/.local/state/boxface/personalisation 2>&1 | head -8
touch /nix/write-test 2>&1 | head -1
df -h /nix | tail -1
GUEST

curl -fsS --unix-socket "$api" -X PUT http://localhost/actions -H 'Content-Type: application/json' -d '{"action_type": "SendCtrlAltDel"}' || true
for _ in $(seq 1 30); do kill -0 "$pid" 2> /dev/null || break; sleep 1; done
kill "$pid" 2> /dev/null || true
ip link del dev "$tap" 2> /dev/null || true
