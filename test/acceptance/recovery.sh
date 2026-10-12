#!/usr/bin/env bash
set -uo pipefail

api="${BOXFACE_API:-http://127.0.0.1:7443}"
token="${BOXFACE_TOKEN:?set BOXFACE_TOKEN to an API token}"
image="${BOXFACE_IMAGE:?set BOXFACE_IMAGE to a ready image ID}"
kernel="${BOXFACE_KERNEL:?set BOXFACE_KERNEL to a vmlinux for the corrupt image}"
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
    -d "{\"name\":\"$1-$run\",\"image_id\":\"${2:-$image}\",\"vcpus\":1,\"memory_mib\":1024,\"disk_gib\":4}"
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
unit_active() { systemctl is-active --quiet "boxface-vm-$1.service"; }
vm_processes() { pgrep -fc "$state/environments/$1/" || true; }
tap_exists() { ip link show dev "bfx$(echo "$1" | tr -d - | cut -c1-12)" > /dev/null 2>&1; }
disk_exists() { [ -f "$state/disks/$1.ext4" ]; }
not() { ! "$@"; }
kill_main() { kill -KILL "$(systemctl show -p MainPID --value "$1")"; }
equals() { [ "$1" = "$2" ]; }

echo "== R1: kill the API during create"
acc="$(create r1)"
op="$(echo "$acc" | field '["operation_id"]')"; r1="$(echo "$acc" | field '["environment_id"]')"
until call "$api/v1/operations/$op" | grep -q create_disk; do sleep 0.05; done
kill_main boxface-api
started=$(date +%s.%N)
check "create resumes after API restart" equals "$(wait_op "$op")" succeeded
echo "      API kill to operation done: $(echo "$(date +%s.%N) - $started" | bc)s"
check "environment running" equals "$(env_field "$r1" observed_state)" running
check "one VM process" equals "$(vm_processes "$r1")" 1
check "disk exists" disk_exists "$(env_field "$r1" disk_id)"

echo "== R2: kill the worker while environments run"
kill_main boxface-worker
check "VM keeps running while the worker is down" unit_active "$r1"
sleep 3
check "worker restarted" systemctl is-active --quiet boxface-worker
check "still one VM process" equals "$(vm_processes "$r1")" 1
systemctl stop boxface-worker
op="$(call -X POST "$api/v1/environments/$r1/stop" | field '["operation_id"]')"
sleep 6
check "stop waits while the worker is stopped" equals "$(call "$api/v1/operations/$op" | field '["state"]')" running
check "VM untouched while the worker is stopped" unit_active "$r1"
systemctl start boxface-worker
check "stop completes after the worker returns" equals "$(wait_op "$op")" succeeded
check "environment stopped" equals "$(env_field "$r1" observed_state)" stopped
check "VM gone" not unit_active "$r1"

echo "== R4: launch fails after disk creation"
corrupt="$state/import/corrupt-$run"
mkdir -p "$corrupt"
cp "$kernel" "$corrupt/vmlinux"
head -c 64M /dev/zero > "$corrupt/rootfs.ext4"
chown -R boxface:boxface "$corrupt"
bad_image="$(call -X POST "$api/v1/images" -d "{\"source\":\"corrupt-$run\"}" | field '["id"]')"
acc="$(create r4 "$bad_image")"
op="$(echo "$acc" | field '["operation_id"]')"; r4="$(echo "$acc" | field '["environment_id"]')"
check "operation fails" equals "$(wait_op "$op")" failed
echo "      error: $(call "$api/v1/operations/$op" | field '["error_message"]' | head -c 160)"
check "environment stopped" equals "$(env_field "$r4" observed_state)" stopped
check "error recorded on the environment" not equals "$(env_field "$r4" error)" ""
check "disk retained" disk_exists "$(env_field "$r4" disk_id)"
check "VM unit removed" not unit_active "$r4"
check "tap removed" not tap_exists "$r4"
check "environment directory removed" not test -e "$state/environments/$r4"

echo "== R5: kill the worker during delete"
acc="$(create r5)"
op="$(echo "$acc" | field '["operation_id"]')"; r5="$(echo "$acc" | field '["environment_id"]')"
check "r5 running" equals "$(wait_op "$op")" succeeded
op="$(call -X DELETE "$api/v1/environments/$r5" | field '["operation_id"]')"
sleep 1
kill_main boxface-worker
check "delete resumes after worker restart" equals "$(wait_op "$op")" succeeded
check "environment deleted" equals "$(env_field "$r5" observed_state)" deleted
check "disk retained" disk_exists "$(env_field "$r5" disk_id)"
check "VM unit removed" not unit_active "$r5"
check "tap removed" not tap_exists "$r5"
check "other environment's disk untouched" disk_exists "$(env_field "$r1" disk_id)"
check "other environment's state untouched" equals "$(env_field "$r1" observed_state)" stopped

echo "== cleanup"
for id in "$r1" "$r4" "$r5"; do
  op="$(call -X DELETE "$api/v1/environments/$id?purge=true" | field '["operation_id"]' 2>/dev/null)" && wait_op "$op" > /dev/null
done

exit "$failed"
