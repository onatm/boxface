#!/usr/bin/env bash
set -uo pipefail

kernel="${BOXFACE_KERNEL:?set BOXFACE_KERNEL to a vmlinux for the corrupt image}"
source "$(dirname "$0")/lib.sh"

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
