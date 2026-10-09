#!/usr/bin/env bash
set -euo pipefail

work="${WORK:-/var/tmp/boxface-gate-a}"
out="$work/out"
data="$work/data.ext4"
api="$work/firecracker.sock"
vsock="$work/vsock.sock"
ready_port=1024
tap=tap0
authorized_keys="${AUTHORIZED_KEYS:-/root/.ssh/authorized_keys}"

start() {
  apt-get install -y socat iptables iproute2

  if [ ! -f "$data" ]; then
    seed="$(mktemp -d)"
    install -d -o 1000 -g 1000 -m 0700 "$seed/home" "$seed/home/.ssh"
    install -o 1000 -g 1000 -m 0600 "$authorized_keys" "$seed/home/.ssh/authorized_keys"
    truncate -s 20G "$data"
    mkfs.ext4 -q -L boxface-data -d "$seed" "$data"
    rm -rf "$seed"
  fi

  if ! ip link show "$tap" > /dev/null 2>&1; then
    ip tuntap add "$tap" mode tap
    ip addr add 172.16.0.1/30 dev "$tap"
  fi
  ip link set "$tap" up
  sysctl -qw net.ipv4.ip_forward=1
  uplink="$(ip route show default | awk '{print $5; exit}')"
  if [ "${NO_NAT:-0}" = 1 ]; then
    iptables -t nat -D POSTROUTING -s 172.16.0.0/30 -o "$uplink" -j MASQUERADE 2>/dev/null || true
  else
    iptables -t nat -C POSTROUTING -s 172.16.0.0/30 -o "$uplink" -j MASQUERADE 2>/dev/null \
      || iptables -t nat -A POSTROUTING -s 172.16.0.0/30 -o "$uplink" -j MASQUERADE
  fi
  iptables -C FORWARD -i "$tap" -o "$uplink" -j ACCEPT 2>/dev/null || iptables -A FORWARD -i "$tap" -o "$uplink" -j ACCEPT
  iptables -C FORWARD -i "$uplink" -o "$tap" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null \
    || iptables -A FORWARD -i "$uplink" -o "$tap" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT

  rm -f "$api" "$vsock" "${vsock}_${ready_port}" "$work/ready.txt"
  cat > "$work/firecracker.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "$out/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off net.ifnames=0 ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$out/rootfs.ext4", "is_root_device": true, "is_read_only": true},
    {"drive_id": "data", "path_on_host": "$data", "is_root_device": false, "is_read_only": false}
  ],
  "machine-config": {"vcpu_count": 2, "mem_size_mib": 2048},
  "network-interfaces": [{"iface_id": "eth0", "guest_mac": "06:00:ac:10:00:02", "host_dev_name": "$tap"}],
  "vsock": {"guest_cid": 3, "uds_path": "$vsock"}
}
EOF

  socat -u "UNIX-LISTEN:${vsock}_${ready_port}" "OPEN:$work/ready.txt,creat,trunc" &
  started=$(date +%s.%N)
  nohup firecracker --api-sock "$api" --config-file "$work/firecracker.json" > "$work/console.log" 2>&1 &
  echo $! > "$work/firecracker.pid"

  for _ in $(seq 1 120); do
    if grep -qx ready "$work/ready.txt" 2>/dev/null; then
      echo "guest ready after $(echo "$(date +%s.%N) - $started" | bc) seconds"
      return
    fi
    kill -0 "$(cat "$work/firecracker.pid")" 2>/dev/null || { echo "firecracker exited; see $work/console.log" >&2; exit 1; }
    sleep 1
  done
  echo "no readiness report after 120 seconds; see $work/console.log" >&2
  exit 1
}

stop() {
  pid="$(cat "$work/firecracker.pid")"
  curl -fsS --unix-socket "$api" -X PUT http://localhost/actions -H 'Content-Type: application/json' \
    -d '{"action_type": "SendCtrlAltDel"}'
  for _ in $(seq 1 60); do
    kill -0 "$pid" 2>/dev/null || { echo "guest stopped"; return; }
    sleep 1
  done
  echo "guest did not stop after 60 seconds" >&2
  exit 1
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
