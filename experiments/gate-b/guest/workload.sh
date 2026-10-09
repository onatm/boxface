#!/usr/bin/env bash
set -euo pipefail

work="$HOME/gate-b"
project=/workspace/gate-b-mise
rm -rf "$work" "$project"
mkdir -p "$work" "$project"

started=$(date +%s)
cargo new --vcs none --quiet "$work/hello"
(cd "$work/hello" && cargo run --quiet) | grep -qx 'Hello, world!'
echo "rust: cargo build and run ok in $(( $(date +%s) - started )) seconds"

node -e 'fetch("https://registry.npmjs.org/").then(r => { if (!r.ok) process.exit(1); console.log("node: https fetch status " + r.status) })'
git ls-remote https://github.com/NixOS/nixpkgs HEAD > /dev/null
echo "git: https ls-remote ok"

profile_major="$(node -p 'process.versions.node.split(".")[0]')"
if [ "$profile_major" = 22 ]; then mise_node=24; else mise_node=22; fi
cd "$project"
MISE_YES=1 mise use "node@$mise_node"
mise_node_path="$(mise exec -- sh -c 'command -v node')"
case "$mise_node_path" in
  "$HOME"/.local/share/mise/*) ;;
  *) echo "mise node not under /home/dev: $mise_node_path" >&2; exit 1 ;;
esac
[ "$(mise exec -- node -p 'process.versions.node.split(".")[0]')" = "$mise_node" ]
echo "mise: project node $mise_node at $mise_node_path (profile node $profile_major)"
for a in claude codex opencode pi; do
  mise exec -- "$a" --version > /dev/null
  echo "$a: starts under mise node $mise_node"
done
echo "mise data: $(du -sh "$HOME/.local/share/mise" | cut -f1)"

docker buildx version
build="$(printf 'FROM busybox\nRUN true\n' | docker build --progress=plain -t gate-b-buildkit - 2>&1)"
grep -q 'load build definition' <<< "$build"
if grep -qi 'legacy builder' <<< "$build"; then echo "docker build used the legacy builder" >&2; exit 1; fi
echo "docker: build uses BuildKit"
