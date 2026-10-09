#!/usr/bin/env bash
set -uo pipefail

source "$(dirname "$0")/lib.sh"

keydir="$(mktemp -d)"
ssh-keygen -q -t ed25519 -N '' -f "$keydir/key"
cp -p /etc/boxface/authorized_keys "$keydir/authorized_keys.orig"
cat "$keydir/key.pub" >> /etc/boxface/authorized_keys
trap 'cp -p "$keydir/authorized_keys.orig" /etc/boxface/authorized_keys; rm -rf "$keydir"' EXIT

now() { date +%s.%N; }
since() { echo "$(now) - $1" | bc; }
count_disks() { ls "$state/disks" | wc -l; }
count_envs() { call "$api/v1/environments" | field '["environments"].__len__()'; }
op_of() { field '["operation_id"]'; }
env_of() { field '["environment_id"]'; }
guest_ip() {
  python3 -c 'import ipaddress,sys; print(ipaddress.ip_interface(sys.argv[1]).ip + 1)' \
    "$(ip -4 -br addr show dev "$(tap_name "$1")" | awk '{print $3}')"
}
guest_ssh() {
  local env="$1"; shift
  ssh -n -i "$keydir/key" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=10 -o LogLevel=ERROR "dev@$(guest_ip "$env")" "$@"
}
dropped() { equals "$(guest_ssh "$1" "timeout 5 bash -c '</dev/tcp/$2/$3' 2>/dev/null; echo rc=\$?")" rc=124; }

echo "== L1: create, then retry with the same idempotency key"
disks_before="$(count_disks)"
started="$(now)"
acc="$(create l1)"; op="$(echo "$acc" | op_of)"; l1="$(echo "$acc" | env_of)"
check "create succeeds" equals "$(wait_op "$op")" succeeded
echo "      create to ready: $(since "$started")s"
retry="$(create l1)"
check "retry returns the same operation" equals "$(echo "$retry" | op_of)" "$op"
check "retry returns the same environment" equals "$(echo "$retry" | env_of)" "$l1"
check "one disk" equals "$(( $(count_disks) - disks_before ))" 1
check "one VM" equals "$(vm_processes "$l1")" 1
check "one tap" equals "$(ip -br link | grep -c "$(tap_name "$l1")")" 1

echo "== L2: reuse the key with different parameters"
envs_before="$(count_envs)"; disks_before="$(count_disks)"
status="$(curl -sS -o "$keydir/l2.json" -w '%{http_code}' -H "Authorization: Bearer $token" -H "Idempotency-Key: l1-$run" \
  -X POST "$api/v1/environments" -d "{\"name\":\"l1-$run\",\"image_id\":\"$image\",\"vcpus\":1,\"memory_mib\":2048,\"disk_gib\":4}")"
check "409 conflict" equals "$status" 409
check "idempotency_conflict code" equals "$(field '["error"]["code"]' < "$keydir/l2.json")" idempotency_conflict
check "no environment created" equals "$(count_envs)" "$envs_before"
check "no disk created" equals "$(count_disks)" "$disks_before"

echo "== L3: stop, start, stop"
guest_ssh "$l1" 'echo l3-marker > /workspace/marker && sync'
started="$(now)"
check "stop succeeds" equals "$(wait_op "$(call -X POST "$api/v1/environments/$l1/stop" | op_of)")" succeeded
echo "      stop: $(since "$started")s"
check "stopped with no VM" not unit_active "$l1"
started="$(now)"
check "start succeeds" equals "$(wait_op "$(call -X POST "$api/v1/environments/$l1/start" | op_of)")" succeeded
echo "      start to ready: $(since "$started")s"
check "running" equals "$(env_field "$l1" observed_state)" running
check "data survived" equals "$(guest_ssh "$l1" 'cat /workspace/marker')" l3-marker
check "second stop succeeds" equals "$(wait_op "$(call -X POST "$api/v1/environments/$l1/stop" | op_of)")" succeeded

echo "== L4: start and stop submitted back to back"
start_op="$(call -X POST "$api/v1/environments/$l1/start" | op_of)"
stop_op="$(call -X POST "$api/v1/environments/$l1/stop" | op_of)"
check "start succeeds" equals "$(wait_op "$start_op")" succeeded
check "start reached readiness" equals "$(call "$api/v1/operations/$start_op" | field '["completed_steps"][-1]')" wait_ready
check "stop succeeds" equals "$(wait_op "$stop_op")" succeeded
check "final state is the last request" equals "$(env_field "$l1" observed_state)/$(env_field "$l1" desired_state)" stopped/stopped
check "no VM" not unit_active "$l1"

echo "== L5: delete one environment, purge another"
a="$(create l5a "" 512)"; b="$(create l5b "" 512)"
l5a="$(echo "$a" | env_of)"; l5b="$(echo "$b" | env_of)"
wait_op "$(echo "$a" | op_of)" > /dev/null; wait_op "$(echo "$b" | op_of)" > /dev/null
l5a_disk="$(env_field "$l5a" disk_id)"; l5b_disk="$(env_field "$l5b" disk_id)"
check "delete succeeds" equals "$(wait_op "$(call -X DELETE "$api/v1/environments/$l5a" | op_of)")" succeeded
check "purge succeeds" equals "$(wait_op "$(call -X DELETE "$api/v1/environments/$l5b?purge=true" | op_of)")" succeeded
check "deleted disk retained on the host" disk_exists "$l5a_disk"
check "deleted environment still listed" bash -c "curl -sS -H 'Authorization: Bearer $token' $api/v1/environments | grep -q $l5a"
check "purged disk removed" not disk_exists "$l5b_disk"
check "purged environment not listed" bash -c "! curl -sS -H 'Authorization: Bearer $token' $api/v1/environments | grep -q $l5b"
check "other environment's disk untouched" disk_exists "$(env_field "$l1" disk_id)"

echo "== L6: create beyond capacity"
limit="$(call "$api/v1/capabilities" | field '["capacity"]["environments"]')"
live="$(call "$api/v1/environments" | python3 -c 'import json,sys; print(sum(e["observed_state"] != "deleted" for e in json.load(sys.stdin)["environments"]))')"
fillers=()
for i in $(seq 1 $(( limit - live ))); do
  acc="$(create "l6-$i" "" 512)"; fillers+=("$(echo "$acc" | env_of)")
  wait_op "$(echo "$acc" | op_of)" > /dev/null
done
check "capacity limit configured" test "$limit" -gt 0
disks_before="$(count_disks)"
status="$(curl -sS -o "$keydir/l6.json" -w '%{http_code}' -H "Authorization: Bearer $token" \
  -X POST "$api/v1/environments" -d "{\"name\":\"l6-over-$run\",\"image_id\":\"$image\",\"vcpus\":1,\"memory_mib\":512,\"disk_gib\":4}")"
check "over-capacity create rejected with 409" equals "$status" 409
check "capacity_exceeded code" equals "$(field '["error"]["code"]' < "$keydir/l6.json")" capacity_exceeded
echo "      $(field '["error"]["message"]' < "$keydir/l6.json")"
check "no disk created" equals "$(count_disks)" "$disks_before"

echo "== S1: guest network reach"
g1="${fillers[0]}"; g2="${fillers[1]}"
host_public="$(ip -4 -br addr show dev "$(ip route show default | awk '{print $5; exit}')" | awk '{print $3}' | cut -d/ -f1)"
check "guest reaches the internet" guest_ssh "$g1" 'curl -fsS -m 15 -o /dev/null https://deb.debian.org'
check "guest cannot reach its gateway's SSH" dropped "$g1" "$(guest_ip "$g1" | python3 -c 'import ipaddress,sys; print(ipaddress.ip_address(sys.stdin.read().strip()) - 1)')" 22
check "guest cannot reach the API port" dropped "$g1" "$(guest_ip "$g1" | python3 -c 'import ipaddress,sys; print(ipaddress.ip_address(sys.stdin.read().strip()) - 1)')" 7443
check "guest cannot reach the host's public address" dropped "$g1" "$host_public" 22
check "guest cannot reach another guest" dropped "$g1" "$(guest_ip "$g2")" 22
check "the other guest is up" guest_ssh "$g2" true

echo "== S2: audit records"
audit() {
  python3 - "$state/api/boxface.db" "$1" <<'PY'
import sqlite3, sys
db = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
print(" ".join(a for (a,) in db.execute("SELECT action FROM audit_events WHERE target_id = ? ORDER BY id", (sys.argv[2],))))
PY
}
check "create recorded once despite the retry" equals "$(audit "$l1")" environment.create
check "create and delete recorded" equals "$(audit "$l5a")" "environment.create environment.delete"
check "create and purge recorded" equals "$(audit "$l5b")" "environment.create environment.delete"
check "API token not stored in the database" bash -c "! grep -aqF '$token' $state/api/boxface.db*"

echo "== cleanup"
for id in "$l1" "$l5a" "${fillers[@]}"; do
  wait_op "$(call -X DELETE "$api/v1/environments/$id?purge=true" | op_of)" > /dev/null
done
check "no VMs left" equals "$(systemctl list-units --plain --no-legend 'boxface-vm-*' | wc -l)" 0
check "no taps left" equals "$(ip -br link | grep -c '^bfx' || true)" 0

exit "$failed"
