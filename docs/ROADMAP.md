# Boxface Roadmap

**Status:** Draft

**Owner:** onatm

**Audience:** Product team

**Format:** Now / Next / Later

**Planning horizon:** Until the personal daily-use trial validates the MVP; Later items are direction, not commitments.

**Last reviewed:** 2026-10-09

**Next review:** When durable server lifecycle exits, or when ThinkCentre measurements contradict the nested-droplet results.

---

## Product Direction

Boxface provides persistent development environments inside microVMs on servers the user controls, so a developer can work from a Linux tablet with native coding agents and return to the same work after disconnecting. The first goal is dependable daily remote development for a single owner on one host. Productisation follows evidence from that use and from other users; a managed cloud marketplace is not the initial scope.

A candidate advantage over SSH, AgentBox, or Coder is a predictable persistent machine with a versioned system/user baseline and low operational burden. YAML, multiple agents, and remote execution alone are not sufficient differentiation.

## Strategic Objectives

| Objective | Target users | Success measure | Strategy source |
|---|---|---|---|
| Replace the laptop for daily development | The owner, travelling with a Linux tablet | At least ten actual development days, over more than one network, without falling back to a local machine because of Boxface defects | [Remote Development Environments](./features/remote-development.md) |
| Keep the baseline fixed and user state persistent | The owner | Feasibility gates A–C pass; system/package-store writes denied; data survives image replacement | [Architecture](./ARCHITECTURE.md), [Baseline Experiment](./plans/2026-10-baseline-experiment.md) |
| Keep operations low-burden | The owner as host operator | Routine use needs no manual edits to host internals; restarts and retries never duplicate or orphan resources | [Operations](./designs/operations.md) |
| Decide productisation with evidence | Prospective developers | Setup effort, recurring use, recovery burden, and demand for hosted compute or team features measured with a small invited group | This roadmap |

## Roadmap Principles

- Validate risky integrations before building product surfaces on them; proposed choices are not validated integrations.
- Never weaken the managed-baseline guarantee to make a workload pass; revisit it as an explicit decision.
- Measure on the actual ThinkCentre hardware before promising startup times, concurrency, or density.
- Build one backend per extension point first: hypervisor, artifact storage, secret provider, and host placement.
- Keep detailed requirements in feature docs and implementation in design docs.

## Horizon Definitions

| Horizon | Meaning | Entry / exit criteria |
|---|---|---|
| Now | Active work with a clear need and plan | Enters when its plan exists; exits when its exit criteria are met |
| Next | Committed MVP stages that depend on Now | Enters Now in order, when the previous stage exits |
| Later | Direction with low confidence | Reconsidered when the MVP trial completes or its trigger fires |

## Roadmap Overview

| Horizon | Outcome / problem | Strategic objective | Success signal | Confidence | Owner |
|---|---|---|---|---|---|
| Now | Environments need a durable lifecycle | Keep operations low-burden | Repeated operations and management-service restarts retain correct ownership and state | Medium: baseline proven; plan written, implementation starting | onatm |
| Next | Work must survive tablet disconnects | Replace the laptop | Disconnects do not terminate running sessions; all four agents validated | Medium: depends on Now | onatm |
| Next | Routine use still needs host internals | Keep operations low-burden | No manual host edits for configuration, upgrades, backups | Medium | onatm |
| Next | The MVP is unproven in real work | Replace the laptop | Ten development days without Boxface-caused fallback | Medium | onatm |
| Later | Unknown whether others value Boxface | Decide productisation | Repeated use by invited developers | Low | onatm |
| Later | Capabilities beyond one owner on one host | Decide productisation | To be defined | Low | onatm |

## Now

### Durable server lifecycle

- **Target outcome:** Environments can be created, listed, started, stopped, and deleted through the API, and survive management-service restarts with correct ownership and state.
- **Problem / opportunity:** A manually launched guest has no durable intent, so retries and restarts duplicate disks or orphan processes.
- **Why now:** The baseline experiment passed on 2026-10-09, and every client feature depends on a durable lifecycle.
- **Evidence:** Gates [A](./research/2026-10-gate-a.md), [B](./research/2026-10-gate-b.md), and [C](./research/2026-10-gate-c.md) validated the guest, profile, container runtime, personalisation, and credential delivery that the lifecycle launches and manages.
- **Committed initiatives:** [Durable Server Lifecycle](./plans/2026-10-durable-lifecycle.md) plan, implemented in Go, milestones M0–M6.
- **Candidate bets:** API, SQLite metadata, worker, image registry, environment disks, asynchronous operations, recovery, and server diagnostics ([Boxface API](./designs/api.md), [Host Worker](./designs/host-worker.md), [Operations](./designs/operations.md)).
- **Success and guardrails:** Retried creates return the same operation; API/worker restarts discover existing guests without duplicate launch. The API never runs client-supplied privileged commands; guests never reach the management plane.
- **Dependencies:** Firecracker confirmed on the ThinkCentre without nested virtualization.
- **Risks / assumptions:** Gate timings come from a nested-virtualization droplet and may not hold on the ThinkCentre. The broker stub had no real guest authorisation; the worker-to-guest channel must provide it.
- **Confidence:** Medium.
- **Owner:** onatm
- **Advance / completion criteria:** The success signal holds under repeated operations and management-service restarts.

## Next

### Daily terminal workflow

- **Target outcome:** From the tablet, the owner connects to a named session, runs any of the four agents and optional mise on real repositories, forwards previews, and reattaches after disconnects.
- **Problem / opportunity:** Lifecycle alone does not let the owner work remotely.
- **Why prioritized:** It is the core of the laptop-replacement objective.
- **Candidate bets:** Authentication, SSH connection discovery, named sessions, port forwarding, and runtime secret bindings ([Access and Sessions](./designs/access-and-sessions.md), [Credential Broker](./designs/credential-broker.md)).
- **Success signal:** Tablet disconnects do not terminate running sessions; project work stays remote and recoverable; Claude Code, Codex, OpenCode, and Pi each complete a small real task with a supported authentication mode.
- **Dependencies:** Durable server lifecycle.
- **Risks / assumptions:** Agent authentication paths differ; at least one supported path per agent must work. Provisional target: session attachment within 3 seconds on a healthy private network.
- **Confidence:** Medium.
- **Owner:** onatm
- **Move-to-Now criteria:** Durable server lifecycle exits.

### Usable configuration and operations

- **Target outcome:** Routine use no longer requires manual edits to host internals.
- **Problem / opportunity:** Without revision management, upgrades, and backups, the owner must operate the host by hand.
- **Candidate bets:** Small [Web UI](./designs/web-ui.md), recipe/profile revision management, explicit image upgrades, offline backup/restore ([Data Lifecycle](./designs/data-lifecycle.md)), resource visibility, actionable logs, and completed recovery and permission checks.
- **Success signal:** The [acceptance scenarios](./features/remote-development.md#acceptance-scenarios) pass, and the [security acceptance](./ARCHITECTURE.md#security-acceptance) checks hold.
- **Dependencies:** Daily terminal workflow.
- **Confidence:** Medium.
- **Owner:** onatm
- **Move-to-Now criteria:** Daily terminal workflow exits.

### Personal daily-use trial

- **Target outcome:** The owner performs their normal selected project work without falling back to a local development machine because of Boxface defects.
- **Problem / opportunity:** A booting microVM is not evidence of dependable daily development.
- **Candidate bets:** Use Boxface for at least ten actual development days, including sessions over more than one network. Record failures, manual interventions, tool/profile changes, and time to recover.
- **Success signal:** Ten days without Boxface-caused fallback. This validates the chosen workloads, not all Linux development.
- **Dependencies:** Usable configuration and operations.
- **Risks / assumptions:** Provisional warm-start target of 15 seconds may not hold on the actual hardware.
- **Confidence:** Medium.
- **Owner:** onatm
- **Move-to-Now criteria:** Usable configuration and operations exits.

## Later

### Productisation evidence

- **Desired outcome:** A small number of invited developers configure their own baseline and use Boxface on their own machines.
- **Why it matters:** Productisation follows evidence; the owner's use alone does not show demand.
- **Current evidence:** None yet. Measure setup effort, recurring use, recovery burden, and demand for hosted compute or team features.
- **Key unknowns:** Whether users value the complete experience over SSH, AgentBox, or Coder.
- **Confidence:** Low.
- **Reconsideration trigger:** Personal daily-use trial passes.

### Beyond one owner on one host

- **Desired outcome:** Capabilities the extension points preserve: multiple workers, remote object storage, hosted compute, browser terminals, snapshots, team access controls, and full OIDC.
- **Why it matters:** These are the likely requirements if productisation proceeds.
- **Key unknowns:** Which capability invited developers need first.
- **Confidence:** Low.
- **Reconsideration trigger:** Productisation evidence shows demand.

## Completed

### Prove the baseline boundaries

- **Completed:** 2026-10-09, in the [Baseline Experiment](./plans/2026-10-baseline-experiment.md).
- **Outcome:** One manually launched Firecracker guest booted a read-only mkosi baseline with a prebuilt Home Manager profile, ran rootless Docker Compose with a database, preserved personal configuration and agent state across restart, and received credentials from 1Password that never entered the image.
- **Evidence:** [Gate A](./research/2026-10-gate-a.md) (guest and container runtime), [Gate B](./research/2026-10-gate-b.md) (prebuilt user profile), [Gate C](./research/2026-10-gate-c.md) (personal configuration and credentials).
- **Carried forward:** Repeat measurements on the ThinkCentre; reduce the 4.7 GiB profile closure; add real guest authorisation to the credential broker; validate Codex, OpenCode, and Pi authentication in the daily terminal workflow.

## Not Now

| Opportunity or request | Reason not prioritized | Reconsideration trigger |
|---|---|---|
| NixOS guest or host | Agreed requirement: conventional Linux guest built with mkosi | None planned |
| Local microVMs on the tablet or macOS virtualisation | The client stays lightweight; execution is server-side | None planned |
| Unified agent chat UI or orchestration | Native agent CLIs are the interface | Users ask for cross-agent workflows |
| Browser terminal streaming | CLI-first terminal access is enough for the owner | Productisation evidence |
| Suspend/resume, memory snapshots, instant forks, live migration | Outside MVP; snapshots may include secrets | Daily-use trial shows start time is a recurring problem |
| Public preview URLs, custom domains, integrated VPN | Loopback forwards over an existing private network suffice | Invited developers need shared previews |
| Multi-host scheduling, teams, billing, marketplace | Single owner on one host | Productisation evidence |
| GPU passthrough and local model serving | Not required for the selected workloads | Workload demand |
| Automatic replay of unfinished agent tasks after reboot | Potentially destructive | None planned |

## Assumptions and Open Questions

| Item | Criticality | Current evidence | Validation / resolution | Owner |
|---|---|---|---|---|
| ThinkCentre hardware supports the needed environments | High | Capacity not measured; gates ran on a nested-virtualization droplet | Measure CPU, memory, and disk on the ThinkCentre before setting lifecycle capacity limits | onatm |
| Firecracker supports rootless Docker and the guest kernel | High | Passed on a nested-virtualization droplet ([Gate A](./research/2026-10-gate-a.md)) | Repeat Gate A on the ThinkCentre; fallback Cloud Hypervisor | onatm |
| Home Manager profile can be exported and exposed read-only on Debian | High | Validated ([Gate B](./research/2026-10-gate-b.md)) | Gate B | onatm |
| Each agent has a supported authentication path in the guest | Medium | Claude Code validated with subscription login and setup token ([Gate C](./research/2026-10-gate-c.md)); others untested | Gate C and the daily terminal workflow | onatm |
| Implementation language | High | Decided: Go | Resolved 2026-10-09 | onatm |

## Review and Change Policy

- Review when each stage exits, and when a feasibility gate fails.
- Move items between horizons when their dependencies exit and open questions are decided.
- Keep detailed requirements in feature docs and implementation in design docs.

## Change Log

| Date | Change | Reason / new evidence | Decision maker |
|---|---|---|---|
| 2026-10-06 | Created the roadmap from the MVP delivery stages and productisation plan | Docs restructured into architecture, design, feature, and plan docs | onatm |
| 2026-10-09 | Completed the baseline stage; moved durable server lifecycle from Next to Now before its plan exists, with the language choice and plan as its first work | Gates A–C passed ([A](./research/2026-10-gate-a.md), [B](./research/2026-10-gate-b.md), [C](./research/2026-10-gate-c.md)) | onatm |
| 2026-10-09 | Chose Go as the implementation language | Owner decision; the lifecycle plan is the remaining first deliverable | onatm |

## Linked Documents

- [Architecture](./ARCHITECTURE.md)
- [Remote Development Environments](./features/remote-development.md)
- [Baseline Experiment](./plans/2026-10-baseline-experiment.md)
