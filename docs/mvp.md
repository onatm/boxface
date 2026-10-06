# Boxface MVP

Status: proposed scope and validation plan  
Date: 2026-10-05  
Companions: [Architecture](architecture.md) · [Detailed design](design.md)

## 1. Outcome

A developer can leave their laptop behind, connect from a Linux tablet to a ThinkCentre-hosted Boxface environment, use their preferred native coding agent, run project services, disconnect, and return to the same work.

The MVP is successful when this is reliable across real development days, not merely when a microVM boots. The first release is for a single owner on one host. It is not a managed multi-tenant service.

## 2. User journey

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

A repository need not contain Boxface configuration. Saved server defaults allow `boxfacectl create NAME` with no supplied file. A server with no default returns a clear selection requirement instead of guessing.

## 3. Required scope

| Area | MVP capability | Acceptance evidence |
| --- | --- | --- |
| Host | One supported Linux/KVM host | Install, diagnose prerequisites, survive management-service restart |
| Guest image | Minimal Debian built by mkosi | Boots under the chosen microVM backend and reports readiness |
| User profile | Prebuilt Home Manager/Nix package closure | Agents and tools available without runtime package installation into baseline |
| Personalisation | Versioned chezmoi configuration | Applies on first initialisation without overwriting agent state on later boots |
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

Browser terminal streaming is not required for the first MVP. The web UI provides connection instructions and lifecycle management; terminal access remains CLI-first.

## 4. Non-goals

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

## 5. Feasibility gates before product implementation

### Gate A: guest and container runtime

Boot a mkosi-built guest with the selected kernel and read-only baseline. Attach a writable data disk. Run SSH, the guest service, and rootless Docker. Build and run a small Compose application with a database and inspect storage permissions.

Pass: normal development works without sudo, rootful Docker access, a writable system overlay, or host Docker socket access.

If Firecracker is the blocker, test Cloud Hypervisor before changing the guest contract. If rootless Docker cannot support necessary workloads, explicitly revisit the immutability/privilege design.

### Gate B: prebuilt user profile

Build the package profile outside the guest. Export all required dependencies. Verify it runs on Debian without allowing the guest user to alter the managed profile or access a privileged Nix daemon. Pin package inputs and confirm cold boot needs no package fetch for declared tools.

Pass: representative agents, mise, Node and Rust execute correctly; project-specific runtime overrides do not break agent launchers.

If Home Manager activation requires excessive mutable machinery, retain its package-generation approach or evaluate a smaller build-time Nix profile compiler. This is an explicit design decision, not permission to move user tools into system packages.

### Gate C: personal configuration and credentials

Apply a chezmoi profile, restart, and verify agent history and local configuration are preserved. Inject a test credential through the server broker. Exercise one actual 1Password binding and one agent login path.

Pass: service-account token never enters the guest; image and build logs contain no real secrets; failure to resolve a binding produces a clear session error.

## 6. Delivery stages

### Stage 1: reproducible baseline experiment

Complete gates A–C with scripts and documented artifacts. No web UI or broad provisioning abstraction. Record kernel options, package versions, idle memory, build duration, and compatibility failures.

Exit: one manually launched guest demonstrates the intended boundaries.

### Stage 2: durable server lifecycle

Implement API, SQLite metadata, worker, image registry, environment disks, asynchronous operations, and recovery. Provide create/list/start/stop/delete and server diagnostics.

Exit: repeated operations and management-service restarts retain correct ownership and state.

### Stage 3: daily terminal workflow

Implement authentication, SSH connection discovery, named sessions, port forwarding, and runtime secret bindings. Validate all four agents and optional mise on actual repositories.

Exit: tablet disconnects do not terminate running sessions; project work stays remote and recoverable.

### Stage 4: usable configuration and operations

Add the small web UI, recipe/profile revision management, explicit image upgrades, offline backup/restore, resource visibility, and actionable logs. Complete recovery and permission checks.

Exit: routine use no longer requires manual edits to host internals.

### Stage 5: personal daily-use trial

Use Boxface for at least ten actual development days, including sessions over more than one network. Record failures, manual interventions, tool/profile changes, and time to recover.

Exit: the owner can perform their normal selected project work without falling back to a local development machine because of Boxface defects. This validates the chosen workloads, not all Linux development.

## 7. Test matrix

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

## 8. Performance and resource criteria

Collect cold-image build time, cached creation time, stopped-to-ready time, session attach latency, idle guest memory, compile performance, disk growth, and backup duration. Separate image build latency from VM boot latency.

Do not publish startup or density claims before measuring the actual ThinkCentre hardware. Initial engineering targets are warm start within 15 seconds and session attachment within 3 seconds on a healthy local/private network, excluding authentication interaction. These are provisional targets, not guarantees or researched benchmark results.

Configure hard memory limits, vCPU limits, maximum environment count, disk quotas, build concurrency, and disk headroom. Reject capacity requests cleanly rather than relying on host out-of-memory recovery.

## 9. Security acceptance

- Guest cannot reach other environments or the management API through unintended routes.
- No host Docker socket, host home directory, or host Nix daemon is exposed.
- Owner credentials are not available to unauthenticated clients.
- Environment create/delete and secret use are auditable without recording secret values.
- Build jobs cannot access runtime secrets.
- Sensitive backup/restore behaviour is documented; no claim that user-data backups are secret-free.
- Rootless Docker and unprivileged sessions preserve the intended managed-baseline boundary.

## 10. Productisation evidence

After personal validation, invite a small number of developers to configure their own baseline and use Boxface on their machines. Measure setup effort, recurring use, recovery burden, and demand for hosted compute or team features.

Proceed toward a product if users repeatedly value the complete experience over SSH, AgentBox, or Coder. YAML, multiple agents, and remote execution alone are not sufficient differentiation. A candidate advantage is a predictable persistent machine with a versioned system/user baseline and low operational burden.
