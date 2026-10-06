#!/usr/bin/env bash
set -uo pipefail

failed=0
check() {
  local name="$1"; shift
  if "$@" > /dev/null 2>&1; then echo "PASS  $name"; else echo "FAIL  $name"; failed=1; fi
}

root_opts="$(findmnt -no OPTIONS /)"
check "root filesystem is ext4" test "$(findmnt -no FSTYPE /)" = ext4
check "root filesystem mounted read-only" grep -qE '(^|,)ro(,|$)' <<< "$root_opts"
check "write to /usr fails with EROFS" sh -c 'touch /usr/.boxface-probe 2>&1 | grep -q "Read-only file system"'
check "write to / fails with EROFS" sh -c 'touch /.boxface-probe 2>&1 | grep -q "Read-only file system"'
check "no writable overlay mounted" test -z "$(findmnt -rn -t overlay)"
check "only data disk is writable persistent storage" sh -c \
  '! findmnt -rno TARGET,FSTYPE,OPTIONS | awk '\''$2 == "ext4" && $3 ~ /(^|,)rw(,|$)/ {print $1}'\'' | grep -vxE "/mnt/data|/home/dev|/workspace|/var/lib/boxface"'
check "/tmp, /run, /var/log are tmpfs" sh -c 'for p in /tmp /run /var/log; do test "$(findmnt -no FSTYPE "$p")" = tmpfs || exit 1; done'

check "running as uid 1000" test "$(id -u)" = 1000
check "only member of the dev group" test "$(id -nG)" = dev
check "sudo absent" sh -c '! command -v sudo'

check "no rootful Docker socket" sh -c '! test -e /var/run/docker.sock && ! test -e /run/docker.sock'
check "rootful docker.service masked" test "$(systemctl is-enabled docker.service 2>/dev/null)" = masked
check "rootful docker.socket masked" test "$(systemctl is-enabled docker.socket 2>/dev/null)" = masked
check "no host filesystem shares" test -z "$(findmnt -rn -t 9p,virtiofs,nfs,nfs4,cifs)"
check "only the root and data disks are attached" test "$(lsblk -dno NAME | sort | tr '\n' ' ')" = "vda vdb "

check "docker daemon is rootless" sh -c 'docker info --format "{{json .SecurityOptions}}" | grep -q rootless'
check "docker uses cgroup v2" test "$(docker info --format '{{.CgroupVersion}}')" = 2
check "docker data root under /home/dev" sh -c 'docker info --format "{{.DockerRootDir}}" | grep -q "^/home/dev/"'
controllers="/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"
check "cpu, cpuset, io, memory, pids delegated" sh -c "for c in cpu cpuset io memory pids; do grep -qw \$c $controllers || exit 1; done"

echo
echo "storage driver: $(docker info --format '{{.Driver}}')"
echo "docker version: $(docker version --format '{{.Server.Version}}')"
echo "rootlesskit network: $(ps -eo args | grep -o -- '--net=[a-z0-9]*' | head -1)"
echo "kernel: $(uname -r)"
echo "setuid binaries:"
find / -xdev -perm -4000 -type f 2>/dev/null | sort | sed 's/^/  /'
echo "memory:"
free -m | sed 's/^/  /'

exit "$failed"
