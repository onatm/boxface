# Baseline Experiment

**Status:** In progress — Gate A passed ([results](../research/2026-10-gate-a.md))

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

### Detailed Plan

Scripts live in `experiments/gate-a/` as plain shell. Keep VMM-specific launch code in one file so a Cloud Hypervisor fallback reuses the kernel, image, and verification steps.

**Host:** A DigitalOcean Basic 2 vCPU / 4 GB droplet using nested virtualization, for feasibility only. DigitalOcean does not officially support nested KVM. Startup, memory, and compile measurements describe a nested guest and must be repeated on the ThinkCentre before they inform targets. Confirm a Firecracker failure on bare metal before treating it as a reason to switch hypervisor.

| Step | Work | Verify |
|---|---|---|
| 0. Host readiness | Provision the droplet; record CPU, RAM, disk, and host distribution; install Firecracker | `grep -cE 'vmx\|svm' /proc/cpuinfo` is non-zero; `/dev/kvm` is usable by the launching user; hardware recorded |
| 1. Guest kernel | Start from Firecracker's published guest config; build in what rootless Docker needs (user namespaces, cgroup v2 controllers, overlayfs, veth, bridge, nf_tables/iptables, tun, seccomp) rather than loading modules from the read-only root | Docker's `check-config.sh` reports no missing required options; config diff from the Firecracker base recorded |
| 2. Baseline image | mkosi Debian image with systemd, openssh-server, docker-ce and rootless extras, uidmap, dbus-user-session, and passt or slirp4netns; fixed `dev` user with stable UID/GID and baked `/etc/subuid` and `/etc/subgid`; no sudo; `dev` not in the `docker` group; raw ext4 filesystem mounted read-only | Image boots with `/` mounted `ro`; no secrets, host keys, or machine ID in the image |
| 3. Writable paths | Declare every writable path the read-only root needs: data disk at `/home/dev` and `/workspace`; tmpfs `/run` and `/tmp`; journald `Storage=volatile`; linger file baked at `/var/lib/systemd/linger/dev`; SSH host keys generated on first boot onto the data disk; transient machine ID | Declared path list recorded; nothing else is writable |
| 4. Launch | Create a sparse ext4 data disk, a tap device with NAT, and a Firecracker config with the rootfs read-only and the data disk read/write; a stub vsock readiness ping stands in for the guest service | Guest reaches SSH; host receives the readiness ping over vsock |
| 5. Boundaries | Assertion script run over SSH | Writes to `/` and `/usr` fail with EROFS; `sudo` absent; no rootful Docker socket; nothing from the host shared into the guest; `docker info` shows rootless, cgroup v2, the storage driver, and a data root under `/home/dev` |
| 6. Workload | Compose application (web service and Postgres) with a named volume and a published port | Application healthy; port reachable from the guest; a written row survives a VM restart; volume ownership on disk matches the subordinate UID mapping |
| 7. Measurements | Kernel options, `dpkg-query` package versions, image build duration and size, cold boot to SSH-ready time, idle memory inside the guest and Firecracker RSS on the host, first `compose up` time | Recorded in `docs/research/2026-10-gate-a.md` |

Guest network isolation from the host management plane is out of scope for Gate A; NAT is used only so the guest can pull images.

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
