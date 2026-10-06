# Baseline Experiment

**Status:** Planned

**Design:** [Architecture](../ARCHITECTURE.md), [Guest](../designs/guest.md), [Image Builder](../designs/image-builder.md), [Credential Broker](../designs/credential-broker.md)

---

## Goal

Prove that one manually launched guest demonstrates the intended boundaries before any product implementation: a read-only mkosi baseline, a prebuilt user package profile, rootless Docker, preserved personal configuration, and server-side credentials.

Use scripts and documented artifacts only. No web UI or broad provisioning abstraction.

## Gates

Each gate records its result in `docs/research/2026-10-<gate>.md`:

| Gate | Outcome | Blocks |
|---|---|---|
| A | Guest and container runtime | Hypervisor choice, guest kernel, Docker storage driver |
| B | Prebuilt user profile | Image builder package-profile design |
| C | Personal configuration and credentials | Personal configuration and credential broker designs |

## Gate A: Guest and Container Runtime

### Steps

1. Boot a mkosi-built guest with the selected kernel and read-only baseline.
2. Attach a writable data disk.
3. Run SSH, the guest service, and rootless Docker.
4. Build and run a small Compose application with a database and inspect storage permissions.

### Pass

Normal development works without sudo, rootful Docker access, a writable system overlay, or host Docker socket access.

### If It Fails

- If Firecracker is the blocker, test Cloud Hypervisor before changing the guest contract.
- If rootless Docker cannot support necessary workloads, explicitly revisit the immutability/privilege design.

## Gate B: Prebuilt User Profile

### Steps

1. Build the package profile outside the guest.
2. Export all required dependencies.
3. Verify it runs on Debian without allowing the guest user to alter the managed profile or access a privileged Nix daemon.
4. Pin package inputs and confirm cold boot needs no package fetch for declared tools.

### Pass

Representative agents, mise, Node and Rust execute correctly; project-specific runtime overrides do not break agent launchers.

### If It Fails

If Home Manager activation requires excessive mutable machinery, retain its package-generation approach or evaluate a smaller build-time Nix profile compiler. This is an explicit design decision, not permission to move user tools into system packages.

## Gate C: Personal Configuration and Credentials

### Steps

1. Apply a chezmoi profile, restart, and verify agent history and local configuration are preserved.
2. Inject a test credential through the server broker.
3. Exercise one actual 1Password binding and one agent login path.

### Pass

Service-account token never enters the guest; image and build logs contain no real secrets; failure to resolve a binding produces a clear session error.

## Measurements

Record kernel options, package versions, idle memory, build duration, and compatibility failures. Where possible also record cold-image build time, stopped-to-ready time, and compile performance on the actual ThinkCentre hardware, separating image build latency from VM boot latency.

## Verification

- Each gate's pass criteria are met, or the failure and the resulting design decision are recorded.
- [Architecture decision status](../ARCHITECTURE.md#decision-status) is updated with validated choices.
- Exit: one manually launched guest demonstrates the intended boundaries.
