# Image Builder

**Status:** Draft — proposed design; package-profile activation is a feasibility gate

---

## Overview

The image builder compiles recipe and profile revisions into immutable, hypervisor-compatible artifact sets: kernel, root filesystem, optional initrd, user package closure, and a resolved build manifest. It runs user-supplied build inputs in isolation from host runtime management and credentials.

## Context & Problem

**Background:** The guest is conventional Linux built with mkosi, while declared developer packages are compiled through Home Manager/Nix before boot. Home Manager's standard activation assumes mutable user profiles and Nix tooling, which conflicts with a read-only baseline on a non-NixOS guest. User-supplied build hooks and Nix expressions are executable input.

## Goals

- Build the user package profile and guest filesystem without installing packages at boot
- Record every resolved input so a deployed artifact is traceable
- Reuse matching artifacts; never let a failed build replace a ready revision
- Isolate executable build input from production credentials and the host

## Technical Design

### Processing Flow

1. Validate authorisation and configuration before fetching private inputs.
2. Resolve pinned sources and compute the build key.
3. Reuse a ready artifact set if the key and compatibility requirements match.
4. Build the user package profile in an isolated builder.
5. Export the profile and complete Nix store closure.
6. Build the Linux filesystem with mkosi, including required system services and guest integration.
7. Assemble the closure at its required `/nix/store` paths and create a platform-controlled executable profile. It may be embedded in the root image for the MVP.
8. Install a controlled personalisation bootstrap and pinned chezmoi source/artifact. Do not include credentials.
9. Produce hypervisor-compatible kernel, root filesystem, and optional initrd.
10. Boot a disposable validation guest, run readiness checks, then atomically publish the artifact set as ready.

Package installation must be complete before developer access is enabled.

### Package Profile Export

The package-profile builder must separate Home Manager's useful package construction from its normal interactive activation workflow. Export the required Nix closure and provide a root-owned profile entry point. Do not expose a writable profile, privileged Nix daemon, or normal `home-manager switch` workflow in the guest.

The feasibility spike must identify the minimal generated artifacts and activation steps required. Do not blindly run an arbitrary activation script as root against a persistent user home. If Home Manager activation requires excessive mutable machinery, retain its package-generation approach or evaluate a smaller build-time Nix profile compiler. This is an explicit design decision, not permission to move user tools into system packages.

### Artifacts and Manifest

Artifacts are stored under immutable identifiers. For the MVP the kernel, root filesystem, optional initrd, user package closure, and manifest may be bundled into one deployable revision while retaining separate logical ownership.

The resolved build manifest records at least:

- Schema version, Boxface builder version, target architecture.
- Canonical recipe and profile revision IDs.
- Distribution package repository snapshot or resolved package versions.
- mkosi version and effective configuration digest.
- Kernel source/version/configuration and required modules.
- Nixpkgs and Home Manager revisions and generated profile expression digest.
- Tool catalogue version, resolved packages, artifact checksums and dependencies.
- chezmoi version, dotfiles commit, and application policy.
- Kernel/rootfs/initrd/profile artifact digests.
- Guest service protocol version and persistent-data compatibility version.
- Build log reference, timestamps, validation results, and provenance.

Hash resolved build inputs for cache lookup; hash final bytes for artifact identity. A recipe hash alone does not prove reproducible output. Avoid using `latest` as a deployable artifact identity. A requested floating version is resolved and recorded.

### Data Model / State

- Image: ID/digest, build key, artifact paths, ready/failed status, compatibility.
- Builds run as `images/build` operations; see [Operations](./operations.md).

### Runtime / Operations

- Quotas and cancellation clean up build resources.
- Failed builds never replace a ready revision; owned temporary resources are removed and previous ready artifacts are preserved.
- Use temporary paths and atomic publication for artifacts.
- Image garbage collection protects artifacts referenced by environments, backups, and active operations.
- Record cold-image build time separately from VM boot latency.

### Security & Privacy

- Hooks may execute arbitrary code, so compilation runs separately from host runtime management and credentials, with resource limits and no broad host filesystem mounts.
- Build jobs receive short-lived access only to required private sources and cannot access runtime secrets.
- Never bake shared host keys, machine IDs, or credentials into an image.

### Key Design Decisions

**Decision 1:** Nix + Home Manager used only in the builder
- **Rationale:** Reuses a mature package-construction ecosystem for user tools while keeping the guest a conventional, non-NixOS Linux.
- **Trade-offs:** Activation on a non-NixOS guest is unvalidated and may need a custom exposure mechanism.

**Decision 2:** Separate cache key from artifact identity
- **Rationale:** Input hashes enable reuse; byte hashes identify what actually runs, without claiming reproducibility.
- **Trade-offs:** Two hashes to compute and store.

## Open Questions

- [ ] Validate Home Manager artifact extraction/activation on Debian and choose a minimal profile exposure mechanism.
- [ ] Select guest kernel and root filesystem format from measured compatibility.

## References

- [Architecture](../ARCHITECTURE.md)
- [Configuration](./configuration.md)
- [Guest](./guest.md)
- [Baseline Experiment](../plans/2026-10-baseline-experiment.md)
- [mkosi image builder](https://github.com/systemd/mkosi)
- [Home Manager standalone setup](https://home-manager.dev/manual/unstable/nix-flakes/standalone.html)
