# User Profiles and Builds

**Status:** Planned — all decisions confirmed by the owner on 2026-10-10

**Roadmap:** [Usable configuration and operations](../ROADMAP.md#usable-configuration-and-operations), candidate bet "recipe/profile revision management"

**Design:** [Image Builder](../designs/image-builder.md), [Configuration](../designs/configuration.md), [Personal Configuration](../designs/personal-configuration.md), [Guest](../designs/guest.md), [Host Worker](../designs/host-worker.md), [Web UI](../designs/web-ui.md)

---

## Goal

A user describes their tools and dotfiles in the web UI, the server builds that profile in an isolated builder microVM, and `boxfacectl create` starts environments from it. Changing tools never rebuilds the base system, and nothing the user writes runs on the host.

## Owner Decisions (2026-10-10)

| Question | Choice |
|---|---|
| How a profile is described | A package picker with a dotfiles repository, plus a raw `home.nix` tab for full Home Manager control |
| Where builds run | A short-lived builder microVM, never the host |
| Layout | One base image for the system and a separate read-only profile drive per profile revision |

## Current State

- The profile is the flake in `experiments/gate-c/profile`. `01-profile.sh` runs `nix build` as root on the host, copies the closure of Home Manager's `home-path` (4.7–5.3 GiB) into a tree, and `02-image.sh` bakes it with mkosi into one 6.7 GiB root image together with the system and the dotfiles. Any tool change means a full image build as root on the host (about 3 minutes warm, much longer cold).
- Images are imported from a staging directory and named by aliases such as `main`. Environments record one image ID.
- The guest finds tools at `/opt/boxface/profile/bin`, a symlink into `/nix/store`; chezmoi applies dotfiles from `/usr/share/boxface/dotfiles` once per data disk.
- The [Image Builder](../designs/image-builder.md) design already calls for isolated builds, a resolved manifest, build keys, and atomic publication; none of it is implemented.

## Layout

```
/dev/vda  base image (ext4, read-only)       system, sshd, Docker, Boxface guest services, boxface-run
/dev/vdb  data disk (ext4, read-write)       /home/dev, /workspace, host key
/dev/vdc  keys drive (raw, read-only)        authorized keys for this start
/dev/vdd  profile drive (ext4, read-only)    mounted at /nix:
                                               /nix/store/…      the home-path closure
                                               /nix/profile      -> /nix/store/…-home-manager-path
                                               /nix/dotfiles/    pinned chezmoi source and revision
```

The base image ships `/opt/boxface/profile -> /nix/profile`, so PATH, wrappers, and agents stay where they are. Store paths are absolute, so the closure must appear at `/nix/store`; mounting the drive at `/nix` gives exactly that without copying.

## Options Considered

### Profile drive format

| Option | Verdict |
|---|---|
| **ext4, read-only** | **Recommended.** Proven in the guest kernel, built with `mkfs.ext4 -d` as the data disk already is |
| EROFS or SquashFS (compressed) | Smaller and faster to copy, but needs the guest kernel option checked; a later optimisation |

### Builder isolation

| Option | Verdict |
|---|---|
| **Firecracker builder VM per build** | **Chosen.** User Nix code runs inside a VM with the same guest firewall (internet only), no credentials, no host mounts, resource limits, and a timeout. The worker attaches an inputs drive, a per-owner Nix cache disk, and an empty output drive, and reads a result over the VM's vsock socket |
| Host build as an unprivileged user | Rejected by the owner: unsafe once anyone else writes a profile |

### Nix cache between builds

| Option | Verdict |
|---|---|
| **Per-owner persistent cache disk** | **Recommended.** Warm builds reuse the owner's previous store; one owner's build cannot plant store paths another owner trusts |
| Shared cache across owners | Faster, but a malicious build could poison store paths others substitute |
| No cache | Every build downloads several GiB from `cache.nixos.org` |

## Decisions

Confirmed by the owner on 2026-10-10.

| Decision | Proposal | Reason |
|---|---|---|
| Profile records | `profiles (id, owner, name)` and immutable `profile_revisions (id, profile, spec, build_key, state, artifact, created)`. Editing creates a revision; environments record the revision they were created from | Matches [Configuration](../designs/configuration.md): revisions are immutable and environments never change silently |
| Picker spec | `{"packages": ["ripgrep", "nodejs_22", …], "unfree": true/false, "dotfiles": {"repo": URL, "ref": REF}}`; package names are nixpkgs attribute paths, checked by the build; suggestions come from a curated list (the four agents, mise, common languages, git, tmux) | A safe default that needs no Nix knowledge, without a full nixpkgs search index |
| Raw spec | `{"module": "<home.nix source>", "dotfiles": …}`: a Home Manager module evaluated with the server's pins. The picker can generate a starting module for the raw tab | Full control for people who know Home Manager |
| Pins | `nixpkgs` and `home-manager` revisions are set by the server (the experiment's current pins first), recorded in each revision's manifest, and updated deliberately by the operator | Reproducible builds; a user cannot pick an arbitrary input source |
| Required tools | The build always adds `tmux`, `chezmoi`, and `git`, which `connect` and personalisation rely on | `connect` and first-boot personalisation must work with any profile |
| Dotfiles | Optional public https git repository and ref. *Changed in P2:* the builder VM fetches the ref and resolves it to a commit, recorded with the revision's build result, instead of the API resolving it when the revision is saved; the API never fetches user-supplied URLs from the host. The dotfiles land in `/nix/boxface` on the profile drive. Private repositories wait for build-time credentials | Removes dotfiles from the base image; keeps every fetch of user input inside the builder |
| Build | Durable `profile.build` operation. The worker starts a builder VM from a builder image (Debian + Nix, built by the experiment scripts like the base image) with: an inputs drive (generated `flake.nix`, the module, pins), the owner's cache disk mounted at `/nix`, and a sized output file. Inside, `nix build` produces `home-path`; a script copies its closure and dotfiles into an ext4 image on the output drive and reports `{status, home_path, closure_mib, log_tail}` on vsock; the unit reboots the VM on success or failure, and a VM that exits without a result is a failed build with its console tail as the log. The worker then checksums and publishes the drive to `profiles/<revision>.img` atomically | Executable input never touches the host; failed builds publish nothing |
| Build limits | 2 vCPUs, 2 GiB memory (the spike's profile needs 1.5 GiB; 1 GiB fails), 60-minute timeout, output capped at 16 GiB, one build at a time per host; the build log tail (last 200 lines) is stored with the revision | Fits the droplet; the ThinkCentre stage can raise them |
| Base image | One base image without a profile: the current system plus `boxface-run`, the keys service, a `nix.mount` unit for `/dev/vdd`, and personalisation reading `/nix/dotfiles`. Built by the experiment scripts and imported as today | System recipes in the UI stay out of scope |
| Environments | `create NAME [--profile NAME]`; defaults gain `profile` (default `main`). *Changed in P2:* a profile's name is the reference, resolved to its latest ready revision at create; no separate alias table is needed. The environment records the base image and profile revision; `VMSpec` gains the profile drive path. Images that still bundle a profile keep working without one | One more default; no change to `connect` |
| Build key and reuse | `sha256(spec canonical JSON, pins, builder version)`; a revision whose key matches a ready one reuses its artifact | The design's cache-key rule; saving an unchanged profile costs nothing |
| Web UI | A Profiles page: list profiles with their revisions and build state; editor with Picker and Raw tabs; Build with live progress and log tail; mark as default. Environments page shows each environment's profile revision and whether the profile has moved | Configuration stays in the web UI; provisioning stays in the CLI |

## Scope

**In scope:** base and builder images, profile drive, profile and revision records, picker and raw specs, builder VM operation with cache and limits, environments from profiles, web UI page, acceptance on the droplet.

**Out of scope:** system recipes and base image builds through the API, private dotfiles or private flake inputs, user-chosen nixpkgs pins, a nixpkgs search index, applying a new profile revision to an existing environment (recreate for now; the [Personal Configuration](../designs/personal-configuration.md) update rules come later), artifact garbage collection, closure size reduction, EROFS.

## Milestones

| Step | Work | Verify |
|---|---|---|
| P0. Spike *(done, [results](../research/2026-10-profile-builder.md))* | `experiments/profile-builder/`: build the current Gate C profile inside a Firecracker VM on the droplet from a Debian + Nix builder image, with a cache disk and an output ext4 drive; boot a guest from a base image without a profile, mount the drive at `/nix`, and run the four agents and chezmoi personalisation | Results in `docs/research/`: cold and warm build times, peak memory, drive size, and whether the guest needs anything beyond the mount. Decides ext4 versus a compressed format and the memory limit |
| P1. Images *(done)* | Base image without a profile, builder image, guest `/nix` mount and personalisation from `/nix/boxface`; worker attaches an optional profile drive. The images are built by `experiments/profile-builder/01-images.sh` | Integration test: a guest boots with a profile drive and runs a tool from it; a bundled image still boots without one |
| P2. Model *(done)* | Migrations, profile and revision API, spec validation, generated flake from the server's pins (`internal/profiles/flake.lock`), build key. Both a picker and a module flake evaluated with Nix 2.35 on the droplet | Unit tests: revisions are immutable; equal specs share a build key; invalid package names, modules over a size limit, and non-HTTPS repositories are rejected before a build |
| P3. Builder | Worker builder RPC and VM lifecycle, per-owner cache disks, durable `profile.build` operation with steps, limits, log tail, atomic publication | Integration test on the droplet: a picker profile builds and publishes; a broken module fails with its Nix error in the log tail and publishes nothing; a timeout kills the VM; the builder cannot reach the host, the LAN, or the tailnet |
| P4. Environments | Profile defaults and aliases, `create --profile`, profile drive in `VMSpec`, environment records | `create` uses the default profile; moving `main` leaves existing environments on their revision |
| P5. Web UI | Profiles page with picker, raw editor, build progress, and log tail | Builds start and report from the browser; cross-origin rules as elsewhere |
| P6. Acceptance | Scenarios below on the droplet | Results in `docs/research/` |

## Acceptance Scenarios

| ID | Scenario | Pass |
|---|---|---|
| U1 | In the web UI, pick `ripgrep`, `nodejs_22`, and the four agents with a public dotfiles repo; build; mark as `main`; `boxfacectl create work`; `connect work` | `rg` and the agents run, dotfiles are applied once, tmux works |
| U2 | Edit the profile to add `jq`; build; create `work2` | `work2` has `jq`; `work` is unchanged and reports that its profile moved |
| U3 | Raw tab: a module that sets `programs.git.userName` and adds a package | Builds and the package runs; `home.packages` content from modules is honoured |
| U4 | A module with a syntax error, and one naming a missing package | Each fails with the Nix error in the log tail; the previous ready revision stays usable |
| U5 | A module whose build tries to reach the host, the LAN, the tailnet, and the host's credential broker | All refused; the build log shows the failures |
| U6 | Build the same spec twice | The second save reuses the artifact without starting a VM |
| U7 | Rebuild after a small change with a warm cache | Measured against the cold build; recorded |

## Open Questions

- How users without Nix knowledge find package names; a search proxied from `search.nixos.org` is a candidate after P5.
- Whether `home.file` and session variables from raw modules should be applied (today only `home.packages` content reaches the guest; Gate B exported `home-path` only).
- Garbage collection of unused profile drives and cache disks, which grow by several GiB per revision.
