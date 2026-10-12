# Guest

**Status:** Draft — proposed design; guest, kernel, and rootless Docker are feasibility gates

---

## Overview

The guest is the development microVM: a read-only managed baseline, persistent mounts for user state, per-environment identity, rootless Docker, and a small guest integration service. It enforces the guarantee that supported developer operations cannot persistently modify the managed baseline.

## Context & Problem

**Background:** Developers need to install dependencies, run Docker Compose, and use agents freely, while the OS and declared toolset stay fixed to a revision. Omitting apt or Home Manager executables alone is not enforcement, and unrestricted rootful Docker access grants guest-root capabilities.

## Goals

- Enforce a read-only baseline without restricting which user-space programs developers run
- Keep user state persistent and baseline drift impossible
- Give each environment its own identity, generated on first initialisation
- Keep agent launchers working regardless of project runtime overrides

## Technical Design

### Filesystem Layout

Use a read-only image plus a persistent environment data disk. Mount writable paths deliberately; avoid a persistent writable overlay over the entire root filesystem, which would conceal baseline changes and preserve system drift.

| Path | Source | Access |
| --- | --- | --- |
| `/` | Versioned root filesystem | Read-only baseline |
| `/nix/store` | Exported package closure | Read-only baseline |
| `/opt/boxface/profile` | Link to resolved package profile | Root-owned, read-only |
| `/home/dev` | Persistent environment data | Guest user read/write |
| `/workspace` | Persistent environment data | Guest user read/write |
| `/run` | tmpfs | Runtime services, sockets, transient credentials |
| `/tmp` | tmpfs with configured limit | Temporary development files |
| Selected service state paths | Explicit persistent or ephemeral mounts | Service-specific ownership |

Keep user home, workspaces, mise downloads, agent history, and rootless Docker data persistent. Keep `/run` and temporary session credentials ephemeral. Any writable service directories require an explicit mount and ownership declaration.

### Immutability Enforcement

Enforcement includes a read-only root device, a read-only package store/profile, an unprivileged guest user, no sudo, and no unrestricted rootful Docker socket. This is not an executable allowlist; see [Architecture](../ARCHITECTURE.md#2-immutable-baseline-persistent-user-state).

### Identity

- One Boxface owner maps to a fixed guest user, initially `dev` with a stable UID/GID. Each environment has its own disk, so the same guest UID across environments is not a shared filesystem identity. Multi-user guests are out of scope.
- Persist guest SSH host identity outside the reusable image. Generate it per environment on first initialisation.
- `/usr/local/bin/boxface-run --consumer NAME -- COMMAND` asks the credential broker over vsock (host CID 2, port 1025) for that consumer's variables and execs the command with them added to its environment; it fails with a message naming the consumer if the broker cannot answer. `/etc/profile.d/zz-boxface-credentials.sh` runs `boxface-run --shell-init` in every login shell (each tmux window `connect` opens): it asks the broker, without waiting, which commands have bindings, writes a shim for each one installed on PATH to `$XDG_RUNTIME_DIR/boxface/bin` (tmpfs), puts that directory first on PATH, exports `session` bindings, and unsets session variables that are no longer bound. A shim runs `boxface-run --command NAME -- REAL "$@"`, so only that command receives its variables. When a Claude Code token is delivered, `boxface-run` also sets `hasCompletedOnboarding` in `~/.claude.json`. If the broker cannot answer, the shell starts without shims and prints one line.
- A base image carries no user tools: it mounts the profile drive (`/dev/vdd`, ext4, `ro,nofail`) at `/nix`, where the closure appears at its own store paths, with `/opt/boxface/profile -> /nix/profile` and `/usr/share/boxface -> /nix/boxface` for the dotfiles source. `boxface-home-files.service`, a user service run at every boot before personalisation, links each file Home Manager generated (`/nix/home-files`) into the home: it creates missing links, refreshes its own, and skips any file the user owns, recording the result in `~/.local/state/boxface/home-files`; personal chezmoi dotfiles applied afterwards win over generated files. Login shells source Home Manager's `hm-session-vars.sh`, so `home.sessionVariables` and `home.sessionPath` apply. Home Manager activation scripts do not run ([profile builder spike](../research/2026-10-profile-builder.md)). Images built before the split bundle the closure in the root filesystem instead.
- `boxface-keys.service` copies the owner's SSH keys from the read-only keys drive (`/dev/vdc`) to `/run/boxface/authorized_keys` before `ssh` starts; `sshd` reads that file and then `~/.ssh/authorized_keys`, so keys the user adds inside the environment still work. Images without this unit accept only the keys a disk was created with, as before.
- Never bake shared host keys, machine IDs, or credentials into a base image. Seed first-boot metadata through a controlled worker-to-guest channel.

### PATH and Agent Runtimes

The user shell exposes the managed profile as a baseline PATH, set in `/etc/environment` for non-interactive SSH commands and in `/etc/profile.d` for login shells, whose `/etc/profile` resets PATH. mise activation can add project-specific paths above it. Managed agent launchers reference their packaged runtime explicitly, so a project's mise-selected Node version cannot break the managed agent launcher.

Prebuilt runtimes that mise downloads link against system libraries, so the baseline carries the libraries they need (`libstdc++6` for Node).

### Locale

The guest uses `LANG=C.UTF-8` and the SSH server does not accept client `LANG` or `LC_*`. C.UTF-8 is built into both Debian's and Nix's glibc, so no locale data is required ([Gate B](../research/2026-10-gate-b.md)).

### Rootless Docker

Rootless Docker uses system-installed binaries. Its storage remains under the developer's persistent data, subject to quota. Verify the chosen storage driver and mount filesystem on the actual guest kernel. Configure the subordinate UID/GID mapping and runtime directory before user access.

If rootless compatibility is insufficient, reconsider the privilege model explicitly; do not silently weaken the guarantee.

### Guest Integration Service

A small Boxface service inside the guest reports readiness, coordinates lifecycle and credentials, and reports session status.

- Use vsock where supported. Bind its control connection to the specific environment and worker; vsock is transport, not sufficient authorisation by itself.
- Keep its privileged interface narrow and launch developer commands under the environment owner's guest UID.
- The worker distinguishes "VMM running" from "guest ready" using this service's readiness report, which retries until delivered.
- The guest's network address comes from the kernel command line (`ip=`), applied by `systemd-network-generator`; the image has no static network configuration.

### Image Contract

An image is a directory with `vmlinux` and `rootfs.ext4`. The worker boots it with the drives and devices below, and any image, including one an owner customises from `images/`, must handle them. `images/rootfs.sh` fails the build when the root filesystem breaks a checked item.

| Worker provides | Image must | Checked |
| --- | --- | --- |
| `/dev/vda`: the root filesystem, read-only | Mount it read-only at `/` | Yes |
| `/dev/vdb`: the data disk, ext4 labelled `boxface-data`, seeded with `boxface/ssh/ssh_host_ed25519_key` | Mount it at `/mnt/data`; `boxface-data.service` bind-mounts `home`, `workspace`, and `boxface` to `/home/dev`, `/workspace`, and `/var/lib/boxface` before SSH and the user session start | Yes |
| `/dev/vdc`: the keys drive, read-only, NUL-padded `authorized_keys` | `boxface-keys.service` copies it to `/run/boxface/authorized_keys` before SSH starts | Yes |
| `/dev/vdd`: the profile drive, read-only, only when the environment has a profile | A base image mounts it at `/nix` (`ro,nofail`) | No |
| `ip=` on the kernel command line, on `eth0` | `systemd-networkd` enabled, with no static network configuration | Yes |
| vsock, guest CID 3; host port 1024 | `boxface-ready.service` sends `ready` once SSH and the data disk are up, retrying until delivered | Yes |
| vsock host port 1025: the credential broker | `/usr/local/bin/boxface-run` (the Go launcher) and the profile script that runs `--shell-init` | Launcher only |
| `boxfacectl connect`: SSH as `dev`, then `tmux new-session -A` | User `dev` (UID/GID 1000, home `/home/dev`); `sshd` using the host key from the data disk and `/run/boxface/authorized_keys`; `socat`; `tmux` on PATH | All but `tmux`, which `profile.sh` checks in the toolset |

The image must also carry no SSH host keys, machine ID, private keys, or `sudo`; the build checks these too.

### Security & Privacy

- Guest workloads are untrusted relative to host management services.
- No host Docker socket, host home directory, or host Nix daemon is exposed to the guest.
- Security-sensitive platform configuration stays outside the user-owned home.

### Key Design Decisions

**Decision 1:** Read-only root plus explicit persistent mounts, not a root overlay
- **Rationale:** Keeps baseline changes visible and impossible to persist.
- **Trade-offs:** Every writable service path must be declared.

**Decision 2:** Rootless Docker
- **Rationale:** Preserves the unprivileged boundary while supporting Compose workloads.
- **Trade-offs:** Storage driver and kernel compatibility must be validated per guest kernel.

## Open Questions

- [x] Select guest kernel, root filesystem format, and Docker storage driver from measured compatibility. Linux 6.18 LTS from Firecracker's guest config plus a Boxface fragment, read-only ext4 root, containerd overlayfs snapshotter ([Gate A](../research/2026-10-gate-a.md)).
- [ ] Persist the machine ID per environment; Gate A used a transient machine ID per boot.

## References

- [Architecture](../ARCHITECTURE.md)
- [Image Builder](./image-builder.md)
- [Personal Configuration](./personal-configuration.md)
- [Data Lifecycle](./data-lifecycle.md)
- [Docker rootless mode](https://docs.docker.com/engine/security/rootless/)
- [Docker privilege implications](https://docs.docker.com/engine/install/linux-postinstall/)
