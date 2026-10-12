api="${BOXFACE_API:-http://127.0.0.1:7443}"
token="${BOXFACE_TOKEN:?set BOXFACE_TOKEN to an API token}"
image="${BOXFACE_IMAGE:?set BOXFACE_IMAGE to a ready image ID}"
state=/var/lib/boxface
run="$(date +%s)"
failed=0

[ "$(id -u)" -eq 0 ] || { echo "run as root on the Boxface host" >&2; exit 1; }

check() {
  local name="$1"; shift
  if "$@" > /dev/null 2>&1; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}
call() { curl -sS --retry 30 --retry-connrefused --retry-delay 1 -H "Authorization: Bearer $token" "$@"; }
field() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }
create() {
  call -X POST "$api/v1/environments" -H "Idempotency-Key: $1-$run" \
    -d "{\"name\":\"$1-$run\",\"image_id\":\"${2:-$image}\",\"vcpus\":1,\"memory_mib\":${3:-1024},\"disk_gib\":4}"
}
wait_op() {
  local op="$1" s
  for _ in $(seq 1 180); do
    s="$(call "$api/v1/operations/$op" | field '["state"]' 2>/dev/null)"
    case "$s" in succeeded|failed|cancelled) echo "$s"; return ;; esac
    sleep 1
  done
  echo timeout
}
env_field() { call "$api/v1/environments/$1" | field "[\"$2\"]"; }
tap_name() { echo "bfx$(echo "$1" | tr -d - | cut -c1-12)"; }
unit_active() { systemctl is-active --quiet "boxface-vm-$1.service"; }
vm_processes() { pgrep -fc "$state/environments/$1/" || true; }
tap_exists() { ip link show dev "$(tap_name "$1")" > /dev/null 2>&1; }
disk_exists() { [ -f "$state/disks/$1.ext4" ]; }
not() { ! "$@"; }
kill_main() { kill -KILL "$(systemctl show -p MainPID --value "$1")"; }
equals() { [ "$1" = "$2" ]; }
