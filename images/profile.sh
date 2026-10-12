#!/usr/bin/env bash
set -euo pipefail

NIX_VERSION=2.35.2
NIX_INSTALLER_SHA256=9adda97297d9e8ab360df95c729eabff4f4f93d6db091953c3a68f29e3fb130c
TOOLS="${TOOLS:-claude codex opencode pi mise node npm rustc cargo cc git tmux}"

here="$(cd "$(dirname "$0")" && pwd)"
flake="${PROFILE_FLAKE:-$here/profile}"
work="${WORK:-/var/tmp/boxface-images/main}"
out="$work/out"
tree="$work/profile-tree"
mkdir -p "$work" "$out"

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

if [ ! -x /nix/var/nix/profiles/default/bin/nix ]; then
  apt-get update
  apt-get install -y curl xz-utils ca-certificates
  curl -fsSL -o "$work/nix-install" "https://releases.nixos.org/nix/nix-${NIX_VERSION}/install"
  echo "${NIX_INSTALLER_SHA256}  $work/nix-install" | sha256sum -c -
  sh "$work/nix-install" --daemon --yes
fi
export PATH="/nix/var/nix/profiles/default/bin:$PATH"
export NIX_CONFIG="experimental-features = nix-command flakes"

start=$(date +%s)
nix build "$flake#homeConfigurations.dev.activationPackage" -o "$work/generation"
end=$(date +%s)

home_path="$(readlink -f "$work/generation/home-path")"
cp "$work/generation/activate" "$out/activate.sh"
find -L "$work/generation/home-files" -type f -printf '%P\n' 2>/dev/null | sort > "$out/home-files.txt" || true

missing=0
for t in $TOOLS; do
  [ -x "$home_path/bin/$t" ] || { echo "missing from home-path: $t" >&2; missing=1; }
done
[ -e "$home_path/bin/home-manager" ] && { echo "home-manager in home-path" >&2; missing=1; }
[ "$missing" -eq 0 ]

nix-store -qR "$home_path" > "$out/closure.txt"
rm -rf "$tree"
mkdir -p "$tree/nix/store" "$tree/opt/boxface"
xargs cp -a -t "$tree/nix/store" < "$out/closure.txt"
ln -s "$home_path" "$tree/opt/boxface/profile"

{
  echo "nix: $(nix --version)"
  nix flake metadata "$flake" | grep -E 'Locked URL|nixpkgs|home-manager'
  echo "unfree allowed: claude-code"
  echo "profile build seconds: $((end - start)) on $(nproc) vcpus"
  echo "home-path: $home_path"
  echo "closure paths: $(wc -l < "$out/closure.txt")"
  echo "closure MiB: $(du -scm $(cat "$out/closure.txt") | tail -1 | cut -f1)"
  echo "nix store paths in closure: $(grep -cE -- '-nix-[0-9]' "$out/closure.txt" || true)"
  echo "nix binaries in home-path: $(ls "$home_path/bin" | grep -xE 'nix|nix-daemon|nix-store|nix-env' | tr '\n' ' ')"
  echo "generated home files: $(wc -l < "$out/home-files.txt")"
} | tee "$out/profile-info.txt"
