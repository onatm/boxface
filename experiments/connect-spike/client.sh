#!/usr/bin/env bash
# Runs the C0 checks from a client on the tailnet against a host started with host.sh.
#   client.sh checks URL SSH_DEST [SSH_OPTION...]   automated checks, about 30 seconds
#   client.sh idle URL SSH_DEST SECONDS [keepalive]  one idle SSH session; run it in its own terminal
#   client.sh attach URL SSH_DEST                   tmux session for the network-switch test
# SSH_DEST is user@anything, for example root@spike; the tunnel decides where SSH lands.
# SPIKE is the client build of experiments/connect-spike (default: spike on PATH).
set -uo pipefail

spike="${SPIKE:-spike}"
mode="${1:-}"; url="${2:?URL required}"; dest="${3:?SSH_DEST required}"
shift 3 || true
ssh_base=(ssh -o "ProxyCommand=$spike tunnel $url/ssh" -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=${TMPDIR:-/tmp}/boxface-spike-known-hosts")
failed=0
check() {
  local name="$1"; shift
  if "$@"; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}
stamp() { date '+%Y-%m-%d %H:%M:%S'; }

case "$mode" in
checks)
  echo "== identity headers"
  who="$(curl -fsS "$url/whoami")"; echo "      $who"
  check "a tailnet user login arrives" python3 -c 'import json,sys; sys.exit(not json.loads(sys.argv[1])["login"])' "$who"
  forged="$(curl -fsS -H 'Tailscale-User-Login: forged@example.com' "$url/whoami")"
  check "a forged login header is replaced" python3 -c 'import json,sys; sys.exit(json.loads(sys.argv[1])["login"] == "forged@example.com")' "$forged"

  echo "== server-sent events through serve"
  out="$("$spike" events "$url/events?n=6")"; echo "$out" | sed 's/^/      /'
  check "events arrive about a second apart, not buffered" python3 -c '
import re,sys
gaps=[float(g) for g in re.findall(r"after ([0-9.]+)s", sys.argv[1])][1:]
sys.exit(not (len(gaps) == 5 and all(0.7 < g < 1.5 for g in gaps)))' "$out"

  echo "== SSH through the upgrade tunnel"
  start=$(date +%s.%N)
  check "ssh runs a command" "${ssh_base[@]}" -o BatchMode=yes "$@" "$dest" true
  echo "      connect and run: $(echo "$(date +%s.%N) - $start" | bc)s"
  check "1 MiB round trip is intact" bash -c "head -c 1048576 /dev/urandom > \"\${TMPDIR:-/tmp}/spike.bin\" && \
    \"\$@\" < \"\${TMPDIR:-/tmp}/spike.bin\" | cmp - \"\${TMPDIR:-/tmp}/spike.bin\"" _ "${ssh_base[@]}" -o BatchMode=yes "$@" "$dest" cat
  exit "$failed"
  ;;
idle)
  seconds="${1:?SECONDS required}"; keepalive="${2:-}"
  opts=(-o ServerAliveInterval=0)
  [ -n "$keepalive" ] && opts=(-o ServerAliveInterval=15 -o ServerAliveCountMax=4)
  echo "$(stamp) start: idle ${seconds}s, keepalive ${keepalive:-off}"
  start=$(date +%s)
  if "${ssh_base[@]}" "${opts[@]}" "$dest" "sleep $seconds; echo survived"; then
    echo "$(stamp) PASS  survived ${seconds}s idle"
  else
    echo "$(stamp) FAIL  dropped after $(( $(date +%s) - start ))s (see journalctl -u boxface-spike on the host for which side closed)"
  fi
  ;;
attach)
  echo "Inside: run 'date; sleep 100000' or similar, switch networks (Wi-Fi to hotspot), wait for the drop,"
  echo "then run this command again and check the same tmux session and process are still there."
  "${ssh_base[@]}" -o ServerAliveInterval=15 -o ServerAliveCountMax=4 -t "$dest" tmux new-session -A -s spike
  ;;
*)
  sed -n '2,7p' "$0"
  exit 2
  ;;
esac
