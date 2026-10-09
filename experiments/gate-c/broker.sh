#!/usr/bin/env bash
set -euo pipefail

BROKER_PORT=1025

here="$(cd "$(dirname "$0")" && pwd)"
work="${WORK:-/var/tmp/boxface-gate-c}"
socket="$work/vsock.sock_${BROKER_PORT}"
token_file="${OP_TOKEN_FILE:-/etc/boxface/op-service-account-token}"
log="$work/broker.log"

lookup() { awk -v id="$1" '$1 == id {print $2, $3}' "$here/bindings"; }

serve() {
  local id binding var ref value started
  read -r id || exit 0
  started=$(date +%s%N)
  result() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) binding=$id result=$1 ms=$(( ($(date +%s%N) - started) / 1000000 ))" >> "$log"; }

  case "$id" in
    ''|*[!a-z0-9-]*) echo "error invalid binding id"; result invalid; return ;;
  esac
  binding="$(lookup "$id")"
  [ -n "$binding" ] || { echo "error binding not approved for this environment"; result denied; return; }
  read -r var ref <<< "$binding"

  if ! value="$(OP_SERVICE_ACCOUNT_TOKEN="$(cat "$token_file")" op read --no-newline "$ref" 2>/dev/null)"; then
    echo "error 1Password lookup failed"; result lookup-failed; return
  fi
  case "$value" in
    ''|*$'\n'*) echo "error resolved value is empty or multi-line"; result bad-value; return ;;
  esac
  echo "ok $var $value"
  result ok
}

case "${1:-}" in
  start)
    [ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
    rm -f "$socket"
    nohup socat "UNIX-LISTEN:$socket,fork" "EXEC:$0 serve" >> "$log" 2>&1 &
    echo $! > "$work/broker.pid"
    echo "broker listening on $socket"
    ;;
  stop)
    kill "$(cat "$work/broker.pid")" 2>/dev/null || true
    rm -f "$socket" "$work/broker.pid"
    echo "broker stopped"
    ;;
  serve) serve ;;
  hash)
    binding="$(lookup "${2:?usage: $0 hash <binding>}")"
    read -r _ ref <<< "$binding"
    OP_SERVICE_ACCOUNT_TOKEN="$(cat "$token_file")" op read --no-newline "$ref" | sha256sum | cut -d' ' -f1
    ;;
  *) echo "usage: $0 start|stop|hash <binding>" >&2; exit 2 ;;
esac
