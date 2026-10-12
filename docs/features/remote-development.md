# Remote Development Environments

**Status:** Draft — MVP scope, not implemented

---

## Overview

A developer can leave their laptop behind, connect from a Linux tablet to a ThinkCentre-hosted Boxface environment, use their preferred native coding agent, run project services, disconnect, and return to the same work.

## Context & Problem

**Background:** The initial user travels with a Xiaomi Pad 6 and owns ThinkCentre machines suitable for hosting development environments. A tablet cannot run the builds, containers, and agents of normal development, and plain SSH to a host offers no versioned baseline, isolation, or managed lifecycle.

The MVP is successful when this is reliable across real development days, not merely when a microVM boots. The first release is for a single owner on one host. It is not a managed multi-tenant service.

## Goals

- Work continues on the server when the tablet disconnects, and the developer returns to the same session
- Claude Code, Codex, OpenCode, and Pi work natively in the environment
- Builds, tests, Docker Compose services, and application previews work without a local development machine
- The baseline stays fixed to a revision, while repositories, dependencies, and agent state persist
- Routine use requires no manual edits to host internals

## Non-Goals

- NixOS guest or host conversion.
- Local microVM execution on the tablet or native macOS virtualisation.
- Running the agent loop locally with every tool redirected remotely.
- Unified agent chat UI, orchestration framework, or agent-specific reasoning features.
- Live migration, suspend-to-disk, memory snapshots, instant forks, or automatic cloud bursting.
- Multi-host scheduling, teams, enterprise identity, billing, or public compute marketplace.
- Public preview routing, custom domains, or integrated VPN implementation.
- GPU passthrough and local model serving.
- Complete prohibition of downloaded or compiled user-space executables.
- Guaranteed bit-for-bit reproducible builds across all external installers.
- Automatic restart or replay of unfinished agent tasks after reboot.
- Browser terminal streaming in the first MVP.

## User Journey

1. Install Boxface on an existing compatible Linux host with KVM and private network access.
2. Register the host with `boxfacectl` and authenticate.
3. Create or import a recipe and user profile containing the desired baseline tools.
4. Build the configured guest image and user package profile.
5. Create a named environment from those revisions.
6. Connect, clone a repository, optionally run mise, and launch Claude Code, Codex, OpenCode, or Pi.
7. Run tests and Docker Compose services; forward an application port to the tablet.
8. Disconnect while the environment and sessions continue running.
9. Reconnect to the same session and inspect results.
10. Stop/start without losing persisted files, and delete only through an explicit data-deletion decision.

## User-Facing Behavior

- A repository need not contain Boxface configuration. Saved server defaults allow `boxfacectl create NAME` with no supplied file.
- The CLI is the terminal path; the web UI manages recipes, profiles, and environments and shows connection instructions.
- Connecting to a stopped environment starts it on the same image, then connects; nothing starts without a client request.
- Previews open on the tablet through a loopback-bound forward, never a public URL.
- When a recipe or profile changes, existing environments show that an update is available and are upgraded only on request.
- Developers can install user-scoped dependencies and runtimes, including through mise, but cannot change the managed OS or declared toolset.

## Business Rules

- One owner, one host. Multiple named environments are allowed, limited by measured host capacity.
- Stopping ends processes but keeps data. Deleting keeps the data disk unless the owner explicitly purges it.
- A server with no default returns a clear selection requirement instead of guessing.
- After a reboot, Boxface never claims a session is still live and never replays an unfinished agent task.
- Credentials are supplied at runtime; none are stored in images. A missing credential fails only the session that needs it.
- Backups are offline and may contain agent login state; restored environments get new identity and do not silently reuse login state.

## Capabilities and Acceptance Evidence

| Area | MVP capability | Acceptance evidence |
| --- | --- | --- |
| Host | One supported Linux/KVM host | Install, diagnose prerequisites, survive management-service restart |
| Guest image | Minimal Debian built by mkosi | Boots under the chosen microVM backend and reports readiness |
| User profile | Prebuilt Home Manager/Nix package closure | Agents and tools available without runtime package installation into baseline |
| Personalisation | Versioned dotfiles in chezmoi format, or a plain dotfiles repository, rendered when the profile is built | Seeded on first initialisation without overwriting agent state on later boots |
| Runtime | One hypervisor backend | Multiple isolated named environments, constrained by measured capacity |
| Persistence | Dedicated data disk per environment | Files and agent state survive clean stop/start and image replacement |
| Access | Native CLI and SSH attachment | Linux arm64 client connects to an x86-64 host without local build dependencies |
| Sessions | Named tmux sessions | Agent continues after client disconnect; reattachment works |
| Agents | Claude Code, Codex, OpenCode, Pi | Each launches and completes a small real task with a supported authentication mode |
| Project tools | Optional mise and ordinary dependency tooling | Installs only into writable user/project paths |
| Docker | Rootless daemon in guest | Representative Compose app builds, starts, persists data, and passes tests |
| Credentials | Scoped runtime bindings and 1Password adapter | Broad service token stays outside guest; no credentials in image artifacts |
| Previews | Explicit CLI port forwarding | Web application opens on tablet through a loopback-bound endpoint |
| Web UI | Minimal recipe/profile and environment management | Same revisions and operations as CLI; create/start/stop/status/build logs |
| Recovery | Durable operations and reconciliation | Failed starts/retries do not duplicate disks or orphan owned processes |
| Data protection | Offline backup and restore | Restore one stopped environment to an independent disk |

## Acceptance Scenarios

| Scenario | Expected result |
| --- | --- |
| Close tablet terminal while agent runs | Process continues; same tmux session can be reattached |
| Tablet sleeps or changes networks | Connection may drop; reattachment succeeds once connectivity returns |
| Stop environment | Processes end cleanly; data retained |
| Start stopped environment | Same files and saved agent state; no false claim of process continuation |
| Restart API/worker | Existing guests discovered; no duplicate launch |
| Host reboot | Persisted disks recover; prior running environments reported stopped until explicitly started |
| Image build fails | No partial image marked ready; previous revisions remain usable |
| Same create request retried | Same operation/environment returned with idempotency key |
| Disk nearly full | Clear failure; reserve/headroom policy prevents silent over-allocation |
| Secret provider unavailable | Binding-dependent launch fails clearly; no unrelated credentials substituted |
| User attempts system/package-store write | Denied |
| User runs mise install | Writes confined to that user's allowed storage |
| User changes project Node version | Managed agent launch still works |
| Data-preserving delete | VM resources removed, retained disk remains discoverable |
| Explicit purge | Correct environment data removed, unrelated disks untouched |
| Offline backup restored | Independent usable copy; restored credentials handled explicitly |

Use two or more representative repositories, including a web application with a database and a native compilation workload. Include browser tests if they are part of actual daily work.

## Impact

- Replaces the laptop for the owner's selected project work while travelling.
- Produces the evidence needed to decide whether Boxface becomes a product; see [Roadmap](../ROADMAP.md).

## Related Docs

- Architecture overview: `docs/ARCHITECTURE.md`
- Roadmap: `docs/ROADMAP.md`
- CLI design: `docs/designs/cli.md`
- Access and sessions design: `docs/designs/access-and-sessions.md`
- Data lifecycle design: `docs/designs/data-lifecycle.md`
