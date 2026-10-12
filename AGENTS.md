Boxface provides persistent development environments inside microVMs running on servers the user controls. A lightweight client, including a Linux tablet, creates an environment, connects to it, and uses native coding-agent CLIs; the environment keeps running when the client disconnects.

## Project Status

Boxface is not implemented yet. The docs describe a proposed architecture and design; the baseline feasibility gates passed (see `docs/research/`), and other integrations are validated as their [Roadmap](./docs/ROADMAP.md) stages run. The implementation is Go, starting with the [lifecycle plan](./docs/plans/2026-10-durable-lifecycle.md).

## Project Documentation

Consult docs only when relevant to the task:

- [Architecture](./docs/ARCHITECTURE.md) - system design, boundaries, data flow, metadata model
- [Roadmap](./docs/ROADMAP.md) - outcomes, horizons, and what is deliberately not prioritized
- [Design Docs](./docs/designs/) - canonical component design docs
- [Feature Docs](./docs/features/) - feature behavior and product decisions
- [Plans](./docs/plans/) - plans to implement
- Research Docs (`docs/research/`) - spike and measurement results, named `YYYY-MM-<topic>.md`; create the directory with the first result

## Operating Principles

### Think Before Coding

- State assumptions explicitly; if uncertain or unclear, ask.
- If multiple interpretations exist, present options/tradeoffs instead of choosing silently.
- Prefer the simplest viable approach and push back on unnecessary complexity.

### Simplicity First

- Implement the minimum that solves the request.
- Do not add unrequested features, abstractions, configurability, or speculative error handling.
- If a simpler solution exists (e.g., 50 lines instead of 200), choose it.

### Surgical Changes

- Touch only requested scope; do not refactor/reformat adjacent code.
- Match existing style.
- If you see unrelated dead code, mention it; do not remove it unless asked.
- Remove only orphans created by your changes (unused imports/vars/functions).
- Every changed line should map directly to the request.

### Goal-Driven Execution

- Define explicit success criteria and verify them.
- Convert requests into testable outcomes:
  - "Add validation": "Write tests for invalid inputs, then make them pass"
  - "Fix the bug": "Write a test that reproduces it, then make it pass"
  - "Refactor X": "Ensure tests pass before and after"
- For multi-step work, use a short plan: `1) step -> verify`.
- Weak goals ("make it work") need clarification.

## Planning and Research

- Use Context7 for official documentation and Exa for discovery/recency.
- `librarian`: external library research; keep guidance tech-agnostic unless the user specifies a stack.
- `architect`: planning and architecture guidance; may delegate deep external research to `librarian`.
- `scribbler`: maintain docs (design docs, feature docs, READMEs, and architecture).
- `ui-ux-reviewer`: review UI/UX for usability, accessibility, and consistency.

## Development Workflow

### Task Commands

`Taskfile.yaml` is the source of truth for build, test, and lint commands. The code is Go.

- `task build` - server binaries for linux/amd64 and `boxfacectl` for linux/amd64, linux/arm64, darwin/arm64, and darwin/amd64, in `bin/`
- `task test` - unit tests
- `task lint` - `gofmt` check and `go vet`
- `task dist` - release archives for each platform and `SHA256SUMS` in `dist/`; pushing a `v*` tag runs it in the release workflow, which publishes a GitHub release with the version's `CHANGELOG.md` section as notes
- `task integration` - builds `bin/linux-amd64/worker.integration.test`; run it as root on a KVM host with `BOXFACE_IT_IMAGE` set to a directory holding `vmlinux` and `rootfs.ext4`; the profile drive test also needs `BOXFACE_IT_BASE` (a base image directory without a profile) and `BOXFACE_IT_PROFILE` (a profile drive), both from `experiments/profile-builder/`
- `deploy/install.sh bin/linux-amd64` - installs `boxface-api` and `boxface-worker` as systemd services on a host, as root
- `test/acceptance/lifecycle.sh` - lifecycle, isolation, and audit scenarios L1–L6, S1, S2 against installed services, as root on the host; needs `BOXFACE_TOKEN` and `BOXFACE_IMAGE`
- `test/acceptance/recovery.sh` - restart scenarios R1, R2, R4, and R5 against installed services, as root on the host; needs `BOXFACE_TOKEN`, `BOXFACE_IMAGE`, and `BOXFACE_KERNEL`

### Post-Implementation Checklist

After implementing any feature or component:
- [ ] Run relevant tests/lint/build commands
- [ ] Record feasibility and measurement results in `docs/research/`
- [ ] Update docs when contracts, state, workflow, or behavior changes
- [ ] Move a validated proposal in [Architecture](./docs/ARCHITECTURE.md#decision-status) from proposed to decided

### Code Style and Component Boundaries

- Follow existing conventions and libraries; do not add comments unless asked.
- Prefer editing existing files; create docs only when asked.
- The API never executes client-supplied privileged commands; only the host worker performs privileged host operations, from validated structured specifications.
- Guests never reach the management plane, other guests, or the host Docker socket.
- Never bake credentials, host keys, or machine IDs into images, and never write secret values to logs or metadata.
- Do not weaken the managed-baseline guarantee to make a workload pass; raise it as an explicit design decision.
