# User Profiles: Builder VM and Profile Drive Spike

**Date:** 2026-10-11

**Result:** Pass. A Home Manager profile built inside a Firecracker builder VM into a read-only ext4 drive; a base image with no profile booted with that drive at `/nix` and ran every tool and the dotfiles from it.

**Plan:** [User Profiles and Builds](../plans/2026-10-profiles.md), step P0

**Scripts:** `experiments/profile-builder/` (`01-images.sh`, `02-build.sh`, `03-boot.sh`)

---

## Summary

The spike separates the system from the user's tools. A 955 MiB base image (the Gate C system with no profile) replaces the 6.7 GiB bundled image, and the profile becomes a 4.8 GiB read-only drive mounted at `/nix`. Store paths are absolute, so mounting the drive at `/nix` makes the closure appear exactly where `/opt/boxface/profile` expects it; the guest needed only that mount point, an `fstab` line, and two symlinks.

The profile builds inside a builder VM that runs a static Nix as root, so the VM, not the host, is the boundary for user Nix code. A cold build downloaded and built the Gate C profile in 127 s and packed it in 83 s; a warm rebuild with one package added took 17 s plus 97 s of packing. Packing, not Nix, dominates warm builds.

## Environment

| Item | Value |
|---|---|
| Host | The droplet: 2 vCPU, 3.8 GiB RAM, nested KVM, Firecracker v1.17.0, guest kernel 6.18.55 |
| Builder image | Debian trixie minimal (systemd, networkd, curl, e2fsprogs) plus the static Nix 2.34.8 from the profile's pinned nixpkgs (`nixStatic`); 387 MiB; read-only root, `/tmp` on tmpfs |
| Builder drives | `vda` builder root (read-only), `vdb` Nix cache (40 GiB sparse, formatted on first use, mounted at `/nix`), `vdc` inputs (flake, lock, dotfiles; read-only), `vdd` output (16 GiB sparse) |
| Builder network | A `bfx` tap in the worker's pool, so the worker's nftables rules apply: internet only |
| Base image | Gate A system with the Gate B and Gate C image trees and `boxface-run`, no profile; `/dev/vdd` at `/nix` (`ro,nofail`), `/opt/boxface/profile -> /nix/profile`, `/usr/share/boxface -> /nix/boxface`; 955 MiB |
| Profile | The Gate C flake (four agents, mise, Node, Rust, git, tmux, chezmoi) and its dotfiles |

## Results

| Check | Result |
|---|---|
| Builder image with static Nix | Pass: 387 MiB, built in 38–44 s |
| Base image without a profile | Pass: 955 MiB, built in 69–73 s (the bundled image is 6.7 GiB) |
| Profile builds in the VM from a read-only inputs drive | Pass: `nix build` of the flake as root with `build-users-group` empty and the sandbox off; home-path closure of 188 paths, 4.8 GiB |
| Output drive | Pass: ext4 with `store/` (the closure, hard-linked from the cache before `mkfs.ext4 -d`), `profile` (symlink to home-path), and `boxface/` (dotfiles and revision); 4.9 GiB used |
| Base boots with the drive at `/nix` | Pass: ready in 6.1 s, as with the bundled image |
| Tools from the drive | Pass: Claude Code 2.1.223, Codex 0.146.0, OpenCode 1.15.10, Pi 0.75.4, mise, Node 24, git, tmux, chezmoi |
| Personalisation from `/nix/boxface` | Pass: applied once with the pinned revision; managed `.bashrc`, `.claude/CLAUDE.md`, `.gitconfig` |
| `/nix` is read-only in the guest | Pass: writes fail with `EROFS` |

## Measurements

| Measurement | Nix build | Pack | Builder VM total |
|---|---|---|---|
| Cold (empty cache, 5.7 GiB downloaded) | 127 s | 83 s | 225 s |
| Warm, unchanged profile | 14 s | 97 s | 149 s |
| Warm, one package added (`jq`) | 17 s | 97 s | 155 s |

| Measurement | Value |
|---|---|
| Cache disk after a build | 7.3 GiB |
| Cold build with 1.5 GiB guest memory | Pass in 204 s (Nix 120 s, pack 82 s), as fast as with 3 GiB |
| Cold build with 1 GiB guest memory | Fails: the kernel's OOM killer kills `patchelf` in a local build step after about 100 s |
| Firecracker peak RSS | Always the full guest memory, because the guest page cache fills it; it says nothing about the build's need |

## Findings

1. **Packing dominates warm builds.** Copying the 4.8 GiB closure into a fresh ext4 drive takes 83–97 s, while a warm `nix build` takes 14–17 s. Reusing the previous drive's unchanged store paths, or a compressed read-only format such as EROFS, is where build time can be won; the closure size reduction already open in [Image Builder](../designs/image-builder.md#open-questions) helps both.
2. **The builder must never write to its console from a service.** In the first full run the build succeeded, but once the serial getty took `ttyS0`, writes from the build service's tty failed, the failure trap's own write failed under `set -e`, and the VM never rebooted until the 60-minute timeout. The builder now masks the getty, logs to the journal and console, and reports and reboots without depending on each other. The product's builder should report over vsock, as the readiness service does.
3. **A static Nix keeps the builder's `/nix` free.** A Nix installed into `/nix` would be hidden by the cache disk mounted there, and the multi-user installer writes to `/etc`, which is read-only. The static binary from the same pinned nixpkgs, at `/usr/local/bin/nix`, needs neither; its first copy into the image was a dangling symlink into the host's store, caught by the first run.
4. **The base image needs `/nix` to exist.** Its read-only root cannot create a mount point at boot; an empty directory in the image is enough.
5. **1.5 GiB is enough; 1 GiB is not.** This profile builds as fast in 1.5 GiB as in 3 GiB; at 1 GiB a local `patchelf` step is OOM-killed. A 2 GiB default leaves margin and lets a build run beside an environment on a 4 GiB host.
6. **An OOM kill must still end the VM.** systemd stopped the whole build service after the OOM kill, so the script's failure trap never ran, and the VM idled until the 60-minute timeout. With `SuccessAction=reboot` and `FailureAction=reboot` on the unit, the same failure ended the VM after 104 s. The script is killed before it can report, so the worker must treat a builder that exits without a result as a failed build and keep the console tail as the log.

## Consequences for the Plan

- ext4 for the profile drive is confirmed; a compressed format is an optimisation for packing time and drive size, not a requirement.
- Builder limits: 2 vCPUs and 2 GiB of memory, not 3 GiB.
- The builder reports its result over vsock and the worker treats a VM exit without a result as a failure; console output is the log.
- The base image needs `/nix`, an `fstab` entry for `/dev/vdd`, `/opt/boxface/profile -> /nix/profile`, and `/usr/share/boxface -> /nix/boxface`; nothing else in the guest changes.
- Packing time is the target for build speed: reuse unchanged store paths from the previous drive, or move to EROFS.

## Builder in the Product (P3)

**Date:** 2026-10-11. **Result:** Pass on the droplet with `boxface-api` and `boxface-worker` from this change and the builder image from `experiments/profile-builder/01-images.sh` (516 MiB with `git` added).

| Check | Result |
|---|---|
| Worker integration test: small picker profile (`jq` plus the required tools), cold per-owner cache | Ready in 1 min 22–35 s; drive published with the `boxface-profile` label |
| A second build while one runs | Refused with "another build is running" |
| Restarting a finished build | No-op; status stays `ready` |
| A module with a syntax error | `failed`, "nix build failed", the Nix syntax error in the log tail, nothing published |
| A build step that connects to the host's SSH port | `failed` with `curl: (28) Connection timed out`: the worker's rules drop builder traffic to the host |
| Through the API: owner's `main` profile with the four agents, mise, Node, Rust, gcc, ripgrep, jq; cold cache | `pending` → `building` → `ready` in 217 s |
| Same packages in another order as a new revision | `ready` within 3 s with no builder VM (same build key) |

**Findings:**

1. **Flakes are evaluated in pure mode,** so `builtins.fetchurl` without a hash is refused before any connection is attempted; network access from user code happens only in build steps, which the firewall confines to the internet.
2. **Published drives were writable by `boxface-vm`,** the user every VMM runs as, so a compromised VMM could have modified another environment's profile; the worker now publishes them owned by root.
3. **Step headings in the log read as errors.** The builder's log marked each step with its failure message; it now logs what it is doing and keeps the failure message for the result only.

## Environments from Profiles (P4)

**Date:** 2026-10-11. **Result:** Pass on the droplet.

The base image (955 MiB, no profile) was imported as `main`, and `boxfacectl create prof` with no flags resolved the owner's `main` profile to revision 2, recorded it on the environment, and was running in 6 s. In a `connect`-style login shell, `/nix` was mounted read-only from `/dev/vdd`; `rg`, `jq`, Node, cargo, mise, and tmux came from `/opt/boxface/profile/bin`; Claude Code answered through its credential shim with the stored token. The guest root filesystem is 922 MiB with 4.7 GiB on the profile drive.

**Finding: the host disk filled.** Staging the base image ran the droplet's 77 GiB disk out of space: four imported 5.4 GiB bundled images, a 5 GiB profile drive, the owner's 7 GiB Nix cache, and about 30 GiB of experiment scratch. SQLite then refused writes (`database or disk is full`) until 13 GiB of spike scratch was removed. Nothing tracks disk used by images, profile drives, or build caches, and nothing removes unused ones; this is recorded as an open question in the plan.

## Profiles Page (P5)

**Date:** 2026-10-11. **Result:** Pass in headless Chromium through `tailscale serve` against real builds, with no console errors.

| Check | Result |
|---|---|
| Add `htop` to `main` in the Packages tab and save | Revision 3 built and showed `ready` with its log after 131 s (warm cache) |
| New profile `ui-check` from a module with a syntax error | Revision 1 showed `failed`, "nix build failed", and the Nix error in the log |
| Environments page | `prof` shows `main #2` with a "#3 ready" badge |

The `ui-check` profile remains on the droplet; profiles cannot be deleted yet.
