#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
work="${WORK:-/var/tmp/boxface-gate-c}"
token_file="${OP_TOKEN_FILE:-/etc/boxface/op-service-account-token}"
credentials=home/.claude/.credentials.json

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
if [ -f "$work/firecracker.pid" ] && kill -0 "$(cat "$work/firecracker.pid")" 2>/dev/null; then
  echo "stop the guest first" >&2; exit 1
fi

secrets="$(mktemp -d /dev/shm/gate-c-leak.XXXXXX)"
mnt="$(mktemp -d)"
cleanup() { umount "$mnt" 2>/dev/null || true; rmdir "$mnt"; rm -rf "$secrets"; }
trap cleanup EXIT
mount -o ro,noload,loop "$work/data.ext4" "$mnt"

add() {
  [ -n "$2" ] || { echo "no value for $1" >&2; exit 1; }
  printf '%s %s\n' "$1" "$2" >> "$secrets/index"
}
add op-token "$(tr -d '\n' < "$token_file")"
export OP_SERVICE_ACCOUNT_TOKEN="$(cat "$token_file")"
for binding in dummy claude-token; do
  ref="$(awk -v id="$binding" '$1 == id {print $3}' "$here/bindings")"
  add "$binding" "$(op read --no-newline "$ref")"
done
unset OP_SERVICE_ACCOUNT_TOKEN
if [ -f "$mnt/$credentials" ]; then
  add claude-access "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["claudeAiOauth"]["accessToken"])' "$mnt/$credentials")"
  add claude-refresh "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["claudeAiOauth"]["refreshToken"])' "$mnt/$credentials")"
fi
cut -d' ' -f2- "$secrets/index" > "$secrets/all"
echo "searching for: $(cut -d' ' -f1 "$secrets/index" | tr '\n' ' ')"

which_secrets() {
  local status=0
  [ -r "$1" ] || { echo "cannot read $1" >&2; return 1; }
  grep -zaoFf "$secrets/all" "$1" > "$secrets/matches" || status=$?
  [ "$status" -le 1 ] || { echo "grep failed on $1" >&2; return 1; }
  tr '\0' '\n' < "$secrets/matches" \
    | awk 'NR == FNR {name[$2] = $1; next} {seen[name[$0]]++} END {for (n in seen) printf "%s(%d) ", n, seen[n]}' "$secrets/index" -
}
files_with_secrets() {
  local status=0
  grep -rlzaFf "$secrets/all" "$@" > "$secrets/files" || status=$?
  [ "$status" -le 1 ] || { echo "grep failed searching $*" >&2; exit 1; }
}

fail=0
must_be_clean() {
  local found
  found="$(which_secrets "$1")" || { echo "FAIL   could not scan $1"; fail=1; return; }
  if [ -n "$found" ]; then echo "FOUND  $1: $found"; fail=1; else echo "clean  $1"; fi
}

must_be_clean "$work/out/rootfs.ext4"
for f in "$work/console.log" "$work/broker.log" /var/tmp/gate-c-*.log /root/.bash_history; do
  [ -f "$f" ] && must_be_clean "$f"
done
files_with_secrets --exclude='*.ext4' --exclude-dir=image --exclude-dir=profile-tree "$work" "$repo"
while IFS= read -r f; do must_be_clean "$f"; done < "$secrets/files"

echo "data disk files:"
files_with_secrets "$mnt"
while IFS= read -r f; do
  found="$(which_secrets "$f")" || { echo "FAIL   could not scan $f"; fail=1; continue; }
  echo "  ${f#"$mnt"/}: $found"
  if [ "${f#"$mnt"/}" != "$credentials" ] || grep -qE 'op-token|dummy|claude-token' <<< "$found"; then fail=1; fi
done < "$secrets/files"
raw="$(which_secrets "$work/data.ext4")" || { echo "FAIL   could not scan $work/data.ext4"; fail=1; }
echo "data disk raw blocks: $raw"

exit "$fail"
