#!/usr/bin/env bash
# Puts the spike server behind tailscale serve on port 8443. Run as root on the host.
#   host.sh start BINARY [TARGET]   TARGET defaults to 127.0.0.1:22 (host sshd); use <guest-ip>:22 for a guest
#   host.sh stop
set -euo pipefail

socket=/run/boxface-spike/api.sock
unit=boxface-spike
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
command -v tailscale > /dev/null || { echo "tailscale is not installed" >&2; exit 1; }

case "${1:-}" in
start)
  bin="$(realpath "${2:?usage: $0 start BINARY [TARGET]}")"
  target="${3:-127.0.0.1:22}"
  echo "tailscale: $(tailscale version | head -1)"
  install -d -m 0700 "$(dirname "$socket")"
  systemctl stop "$unit" 2> /dev/null || true
  systemd-run --unit "$unit" --collect -- "$bin" server -socket "$socket" -target "$target"
  for _ in $(seq 1 20); do [ -S "$socket" ] && break; sleep 0.25; done
  [ -S "$socket" ] || { journalctl -u "$unit" --no-pager | tail; exit 1; }
  if ! tailscale serve --bg --https=8443 "unix:$socket"; then
    echo "tailscale serve rejected the unix: target; this Tailscale is too old for C0 as planned" >&2
    exit 1
  fi
  tailscale serve status
  dns="$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')"
  echo
  echo "URL: https://$dns:8443"
  echo "server log: journalctl -fu $unit"
  ;;
stop)
  tailscale serve --https=8443 off || true
  systemctl stop "$unit" 2> /dev/null || true
  rm -rf "$(dirname "$socket")"
  ;;
*)
  echo "usage: $0 start BINARY [TARGET] | stop" >&2
  exit 2
  ;;
esac
