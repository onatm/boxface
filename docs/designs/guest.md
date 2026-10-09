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
- The worker distinguishes "VMM running" from "guest ready" using this service's readiness report.

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
