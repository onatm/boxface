# Configuration

**Status:** Draft — proposed interface; examples are not an existing API

---

## Overview

Boxface creates a named persistent microVM from a versioned system recipe and a versioned user profile. This doc defines the configuration schema, how definitions are validated and stored, and how floating inputs resolve to immutable revisions.

## Context & Problem

**Background:** The fixed baseline and mutable user state must remain distinguishable in configuration, filesystem layout, lifecycle, and status output. Configuration must therefore separate system requirements from user tools, and must never let a later change to an external source silently alter an existing environment.

## Goals

- One versioned, strictly validated schema shared by the CLI and web UI
- Clear separation of system packages, declared user tools, personal files, runtime sizing, and credentials
- Floating inputs resolved once and recorded per revision
- Saved recipes and profiles reusable by ID, with predictable override rules

## Technical Design

### Schema Handling

Use a versioned YAML schema. Store submitted source and a canonical validated representation. Reject unknown fields, invalid types, duplicate keys, and incompatible combinations. Never interpret arbitrary YAML tags as executable objects.

### Example Bundle

Version labels and repository names are illustrative; the resolver must convert them to actual package inputs and immutable source revisions before a build becomes deployable.

```yaml
apiVersion: boxface.dev/v1alpha1
kind: EnvironmentDefinition
metadata:
  name: development
spec:
  system:
    distribution: debian
    release: trixie
    architecture: x86_64
    services:
      ssh: true
      docker:
        mode: rootless
    packages:
      - ca-certificates
      - uidmap
  profile:
    name: personal
    tools:
      - id: claude-code
      - id: codex
      - id: opencode
      - id: pi
      - id: mise
      - id: nodejs
        version: "22"
      - id: rust
      - id: git
      - id: tmux
    dotfiles:
      manager: chezmoi
      source: https://git.example.com/user/dotfiles.git
      revision: main
  runtime:
    cpus: 4
    memoryMiB: 8192
    dataDiskGiB: 80
  credentials:
    - binding: anthropic-development
      scope: session
      consumer: claude-code
      delivery:
        environmentVariable: ANTHROPIC_API_KEY
```

### Interpretation

- `system.packages` contains administrative/system requirements. User-facing agents, language runtimes, and mise belong in `profile.tools`.
- A curated tool catalogue maps stable tool IDs to tested package definitions and launcher metadata. Missing catalogue support is an explicit build error, not an unpinned install script at boot.
- `version` is a requested constraint; resolved inputs record exact versions and sources.
- `dotfiles.revision: main` is resolved once to a commit for the profile revision. A later change to the branch cannot silently alter an existing environment.
- `credentials.binding` references a server-approved binding. A repository cannot grant itself vault access by naming a new reference. See [Credential Broker](./credential-broker.md).
- Runtime sizing does not normally require rebuilding package artifacts. Architecture, kernel requirements, package inputs, and system configuration do.
- Docker cannot be configured as unrestricted rootful access in the initial immutable-baseline mode.

### Saved Definitions and Overrides

Saved recipes and profiles can be referenced by ID instead of embedded in a bundle. For the MVP, support the bundle plus saved selections; avoid a general inheritance language.

- Explicit CLI selections override the `default` template.
- Resource overrides can override saved sizes within host limits.
- Reject ambiguous duplicate definitions instead of silently merging nested security configuration.
- Server defaults allow creating an environment with no supplied file. A new environment resolves defaults once and records exact revisions. A server with no default returns a clear selection requirement instead of guessing.

### Data Model / State

- Recipe revision: ID, source YAML, canonical spec, parent/name, creation time.
- Profile revision: ID, tools, pinned dotfiles, package inputs.
- Editing a recipe or profile creates a new revision. Existing environments show that an update is available; they do not change silently.

### Security & Privacy

- Recipes with scripts, Nix expressions, or chezmoi hooks are executable input and require server-side authorisation.
- Configuration never contains secret values; it references server-approved bindings only.

### Key Design Decisions

**Decision 1:** Strict schema with stored source and canonical form
- **Rationale:** Rejecting unknown and duplicate fields prevents silent misconfiguration of security-relevant settings; the canonical form makes revisions comparable.
- **Trade-offs:** Schema evolution requires explicit versioning.

**Decision 2:** No inheritance language for the MVP
- **Rationale:** Saved selections plus explicit overrides cover the single-owner use case without ambiguous merges.
- **Trade-offs:** Sharing partial definitions requires duplication until a composition model is justified.

## Open Questions

- [ ] Confirm agent package availability, licensing constraints, and release pinning for the tool catalogue.

## References

- [Architecture](../ARCHITECTURE.md)
- [Image Builder](./image-builder.md)
- [Personal Configuration](./personal-configuration.md)
- [mise configuration](https://mise.jdx.dev/configuration.html)
