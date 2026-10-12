#!/usr/bin/env bash
set -euo pipefail

FC_VERSION=v1.17.0
FC_SHA256=06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558
ARCH=x86_64

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

[ "$(grep -cE 'vmx|svm' /proc/cpuinfo)" -gt 0 ] || { echo "no hardware virtualization flags" >&2; exit 1; }
[ -r /dev/kvm ] && [ -w /dev/kvm ] || { echo "/dev/kvm not usable" >&2; exit 1; }

apt-get update
apt-get install -y curl ca-certificates

tarball="firecracker-${FC_VERSION}-${ARCH}.tgz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$tarball" "https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/${tarball}"
echo "${FC_SHA256}  $tmp/$tarball" | sha256sum -c -
tar -xzf "$tmp/$tarball" -C "$tmp"
install -m 0755 "$tmp/release-${FC_VERSION}-${ARCH}/firecracker-${FC_VERSION}-${ARCH}" /usr/local/bin/firecracker
install -m 0755 "$tmp/release-${FC_VERSION}-${ARCH}/jailer-${FC_VERSION}-${ARCH}" /usr/local/bin/jailer

{
  echo "## host"
  date -u +%Y-%m-%dT%H:%M:%SZ
  grep -m1 'model name' /proc/cpuinfo
  echo "vcpus: $(nproc)"
  free -h | awk '/^Mem:/ {print "memory: " $2}'
  df -h / | awk 'NR==2 {print "root disk: " $2 " (" $4 " free)"}'
  . /etc/os-release && echo "distro: $PRETTY_NAME"
  echo "kernel: $(uname -r)"
  echo "firecracker: $(firecracker --version | head -1)"
} | tee host-info.txt
