# Baseline Experiment

**Status:** In progress — Gate A passed ([results](../research/2026-10-gate-a.md)); Gate B passed ([results](../research/2026-10-gate-b.md))

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

### Detailed Plan

Scripts live in `experiments/gate-b/` as plain shell plus one Nix flake. Reuse Gate A's kernel, image configuration, and launch script; Gate B adds a profile build and injects the exported closure into the image tree before `mkfs.ext4`. The closure is embedded in the root image, as the [Image Builder](../designs/image-builder.md#processing-flow) allows for the MVP, so `/nix/store` inherits the read-only root.

**Approach:** Use Home Manager only as a package generator. Build the standalone `activationPackage` and expose only its `home-path` (the `buildEnv` of `home.packages`) as `/opt/boxface/profile`. Do not run the activation script in the guest; inspect it and record what it would do. The gate answers whether `home-path` alone is enough.

**Host:** The Gate A droplet, which also acts as the builder. Builder isolation is out of scope for Gate B.

| Step | Work | Verify |
|---|---|---|
| 0. Builder | Install a pinned Nix release on the host with flakes enabled | Nix version recorded; no Nix installed in the guest image |
| 1. Profile flake | Flake pinning nixpkgs `nixos-26.05` and Home Manager `release-26.05` by commit, with a standalone `homeConfigurations.dev` (user `dev`, home `/home/dev`) declaring Claude Code, Codex, OpenCode, Pi, mise, Node, Rust (`rustc`, `cargo`, and `gcc` as the linker), Git, and `cacert`. Allow unfree only for the packages that need it | Each agent's availability in the pinned nixpkgs recorded; a missing agent is packaged in the flake or recorded as a gap; unfree packages and licences recorded |
| 2. Build and inspect | Build `activationPackage`; list the activation script's actions and generated files | `home-path/bin` contains every declared tool; activation actions recorded with which ones `home-path` makes unnecessary |
| 3. Export | Compute the closure of `home-path` with `nix-store -qR`; copy it into the image tree at `/nix/store`, root-owned; create `/opt/boxface/profile` as a root-owned symlink to `home-path`; no Nix database, daemon, or channels | Closure path count and size recorded; `nix`, `nix-daemon`, and `home-manager` absent from the image unless a declared package pulls them in, in which case record which |
| 4. Shell exposure | Put `/opt/boxface/profile/bin` first on the baseline PATH through `/etc/environment` (Gate A finding 3); set `SSL_CERT_FILE` and `LOCALE_ARCHIVE` there if Nix-built tools need them | `ssh dev@guest 'command -v claude codex opencode pi mise node cargo'` resolves to `/nix/store` paths in non-interactive and login shells |
| 5. Boundaries | Rerun Gate A's `verify-boundaries.sh`, plus profile checks | All Gate A checks still pass; writes under `/nix/store` and `/opt/boxface` fail with EROFS; no `/nix/var/nix/daemon-socket`; `dev` cannot replace the profile; no `~/.nix-profile`, `~/.local/state/home-manager`, or Home Manager-generated files in `/home/dev` |
| 6. Offline boot | Launch with `NO_NAT=1` | Every declared tool starts and reports its version with no network; a guest without network fetches nothing at boot |
| 7. Workload | With NAT enabled: compile and run a small Rust crate with `cargo`; run a Node script; `mise use` a different Node version in a `/workspace` project and run the four agent launchers from inside it; mise runtimes land under `/home/dev` | Rust and Node work; inside the project `node --version` reports the mise version while every agent launcher still starts, its interpreter path pointing into `/nix/store`; agent authentication is Gate C |
| 8. Gate A carryover | Confirm `docker-buildx-plugin` (Gate A finding 4) | `docker buildx version` works and the Gate A Compose build uses BuildKit |
| 9. Measurements | Profile build time (cold and cached), closure size and path count, rootfs size and image build time compared with Gate A, launch-to-readiness time, idle guest memory | Recorded in `docs/research/2026-10-gate-b.md` with nixpkgs and Home Manager revisions |

### Pass

Representative agents, mise, Node and Rust execute correctly; project-specific runtime overrides do not break agent launchers.

### If It Fails

If Home Manager activation requires excessive mutable machinery, retain its package-generation approach or evaluate a smaller build-time Nix profile compiler. This is an explicit design decision, not permission to move user tools into system packages.

## Gate C: Personal Configuration and Credentials

### Steps

1. Apply a chezmoi profile, restart, and verify agent history and local configuration are preserved.
2. Inject a test credential through the server broker.
3. Exercise one actual 1Password binding and one agent login path.

### Detailed Plan

Scripts live in `experiments/gate-c/` as plain shell. Reuse Gate A's kernel and launch script and Gate B's profile and image steps; Gate C's flake is Gate B's plus `chezmoi`.

**Personalisation:** A small chezmoi source in `experiments/gate-c/dotfiles/`, identified by a content digest recorded as its revision, is embedded read-only in the image at `/usr/share/boxface/dotfiles`. A systemd user unit applies it once, on first initialisation of a data disk, and records the applied revision and managed file list under `~/.local/state/boxface/`. Later boots never reapply it. The source manages `~/.gitconfig`, a shell snippet, and `~/.claude/CLAUDE.md`, an agent configuration file kept beside agent-written state (`~/.claude.json`, `~/.claude/projects/`) that chezmoi must not touch.

**Broker stub:** A host script stands in for the credential broker. The guest asks for a binding by ID only; the host checks the ID against an approved bindings file, resolves its 1Password reference with `op read`, and returns the value on the same connection. The guest connects over vsock to host port 1025, which Firecracker exposes on the host as a per-VM Unix socket; the socket path identifies the environment, a stand-in for real authorisation. A guest launcher, `boxface-run <binding>... -- <command>`, sets each value only in the child's environment and then runs the command. No secret is passed on a command line or written to a file in the guest.

**Agent login paths:** Two Claude subscription paths; no Anthropic API key is used.

- *Broker-delivered token:* the owner runs `claude setup-token` on a machine with a browser and stores the long-lived token in 1Password. The `claude-token` binding delivers it as `CLAUDE_CODE_OAUTH_TOKEN` to a Claude Code run in a temporary home, so no stored login can mask the token.
- *Interactive login:* `claude` logged in over SSH. The guest has no browser, so the login URL is opened on the client and the code pasted back. Claude Code stores the login state in the persistent home, expected at `~/.claude/.credentials.json`. This is agent-managed state, not a broker binding.

**Host:** The Gate A and B droplet.

| Step | Work | Verify |
|---|---|---|
| 0. Secrets setup | Owner creates a 1Password vault for the gate, a service account limited to it, and two items: a generated dummy value and the `claude setup-token` output. Store the service-account token in a root-only file outside the repository and work directories; install a pinned 1Password CLI | `op whoami` succeeds as root using the token file; the vault, items, and CLI version recorded, never values |
| 1. Image | Gate B profile plus `chezmoi`; image adds the dotfiles source, the personalisation user unit, and `boxface-run` | Image builds; chezmoi resolves to the profile; a scan of the image, build logs, and work directory finds neither the token nor either item's value |
| 2. First initialisation | Boot on a fresh data disk | Managed files have the expected content; the state record names the dotfiles revision and lists the managed files; agent state paths are not in that list |
| 3. Delivery | Host broker serving `${vsock}_1025`; bindings file mapping each binding ID to a 1Password reference and environment variable name | `boxface-run dummy -- <check>` sees the variable, and its SHA-256 matches the host's hash of `op read`; the variable is absent from the login shell and from every process's command line; no file under `/home`, `/workspace`, `/tmp`, or `/run` contains the value |
| 4. Failure | Request an unapproved binding ID, a binding whose 1Password item does not exist, and any binding with the broker stopped | Each fails before the command starts, with an error naming the binding and the reason; no fallback credential is used |
| 5a. Setup token | `HOME=<temporary> boxface-run claude-token -- claude -p "Reply with OK"` | Claude Code answers; no login credentials file is written; no guest file contains the token |
| 5b. Interactive login | Owner runs `claude` over `ssh -t` and logs in with the Claude subscription, then runs `claude -p "Reply with OK"` | Login completes without a browser or keyring in the guest; Claude Code answers; login state, `~/.claude.json`, and project history are created under `/home/dev`; the credential file's path and mode are recorded |
| 6. Restart | Edit `~/.claude/CLAUDE.md`, then stop and start the guest | Personalisation is not reapplied; the edit, `~/.claude.json`, and Claude Code's project history survive; `claude -p` still answers without logging in again; `chezmoi status` reports the edited file, recorded as input to the conflict-workflow open question |
| 7. Leak scan | On the host, search the root image, data disk image, console log, build logs, broker output, and work directory for the service-account token and both item values; read the Claude Code access and refresh tokens from the guest's credential file and search for them too | The service-account token appears nowhere outside its file; neither item value is on either disk image or in any log; the Claude Code access and refresh tokens are only in its credential file on the data disk, never in the root image or any log |
| 8. Measurements | Time each broker resolution, personalisation apply time on first boot, and versions of chezmoi and the 1Password CLI | Recorded in `docs/research/2026-10-gate-c.md` |

Updating a seeded home from a newer dotfiles revision is out of scope; it remains the [Personal Configuration](../designs/personal-configuration.md#open-questions) open question.

### Pass

Service-account token never enters the guest; image and build logs contain no real secrets; failure to resolve a binding produces a clear session error.

## Measurements

Record kernel options, package versions, idle memory, build duration, and compatibility failures. Where possible also record cold-image build time, stopped-to-ready time, and compile performance on the actual ThinkCentre hardware, separating image build latency from VM boot latency.

## Verification

- Each gate's pass criteria are met, or the failure and the resulting design decision are recorded.
- [Architecture decision status](../ARCHITECTURE.md#decision-status) is updated with validated choices.
- Exit: one manually launched guest demonstrates the intended boundaries.
