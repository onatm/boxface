# Durable Server Lifecycle

**Status:** In progress — decisions confirmed 2026-10-09

**Roadmap:** [Durable server lifecycle](../ROADMAP.md#durable-server-lifecycle)

**Design:** [Boxface API](../designs/api.md), [Host Worker](../designs/host-worker.md), [Operations](../designs/operations.md), [Data Lifecycle](../designs/data-lifecycle.md), [Guest](../designs/guest.md), [boxfacectl](../designs/cli.md)

---

## Goal

Environments can be created, listed, inspected, started, stopped, and deleted through the API, and survive API, worker, and host restarts with correct ownership and state. Retried creates return the same operation; restarts never duplicate a launch, a disk, or a network device.

The guest, profile, and credential boundaries are already proven by the [Baseline Experiment](./2026-10-baseline-experiment.md). This stage builds the first Go code around them.

## Scope

**In scope:**

- Owner bootstrap and API token authentication.
- Image registration of a prebuilt artifact set (kernel, root filesystem, manifest).
- Environment create, list, inspect, start, stop, delete, and purge as durable operations with idempotency keys and resumable events.
- Host worker with one Firecracker backend: data disks, per-environment networking with isolation, VM launch and stop, guest readiness, inventory, and reconciliation.
- Capacity limits on environment count, vCPUs, memory, and disk.
- Audit records for environment create and delete.
- A `boxfacectl` subset for the lifecycle commands.

**Out of scope:** image builds through the API, recipe and profile revisions, SSH gateway and sessions, port forwarding, credential broker integration, backup, restore, upgrade, and the web UI. These belong to later [roadmap](../ROADMAP.md) stages. Until then, images come from the experiment scripts and the owner reaches guests through the host as in the gates.

## Decisions

Confirmed by the owner on 2026-10-09; each is the simplest choice that meets the designs.

| Decision | Proposal | Reason |
|---|---|---|
| Processes | `boxface-api` runs as an unprivileged `boxface` user and owns SQLite. `boxface-worker` runs as root and holds no database. `boxfacectl` is the client | Keeps privilege in the worker only ([Host Worker](../designs/host-worker.md)) |
| API–worker interface | JSON over HTTP on a Unix socket readable only by the `boxface` group; the worker checks the peer's UID with `SO_PEERCRED` | A local authenticated interface with no network exposure |
| HTTP and RPC | Go standard library `net/http`; server-sent events for operation events | Closes the [API open question](../designs/api.md#open-questions) without a framework |
| SQLite | `modernc.org/sqlite` (pure Go), WAL mode, embedded schema migrations | No cgo in the server build |
| VM processes | Each Firecracker VM runs in a systemd transient unit, `boxface-vm-<environment-id>.service`, as an unprivileged `boxface-vm` user with `/dev/kvm` and its tap device | VMs survive worker restarts; the unit name gives ownership; a host reboot ends them; cgroup limits apply. The jailer is deferred |
| Guest addressing | Each environment gets a `/30` from a configured pool, passed on the kernel command line (`ip=`) and applied by `systemd-network-generator`; the image's static `10-eth0.network` is removed | One image serves every environment |
| Isolation | nftables: guests may reach the internet; guest traffic to the host and to other guests is dropped | [Security acceptance](../ARCHITECTURE.md#security-acceptance) |
| Readiness | The image's vsock readiness report, received by the worker on each VM's own socket | Reuses the Gate A mechanism; the full guest integration service comes later |
| Worker state | Discovered from systemd units, tap devices, and the per-environment directory `/var/lib/boxface/environments/<id>/`; the API is the only writer of intent | One source of truth for intent; actual resources are always observed |

## Milestones

| Step | Work | Verify |
|---|---|---|
| M0. Skeleton | Go module, `cmd/boxface-api`, `cmd/boxface-worker`, `cmd/boxfacectl`; `Taskfile.yaml` with `build`, `test`, `lint` (`go vet`, `gofmt -l`); listed in `AGENTS.md`; decisions recorded in the design docs | `task build test lint` passes; binaries cross-build for linux/amd64 (server) and linux/arm64 (client) |
| M1. Metadata and operations | SQLite schema for owner, image, environment, data disk, operation, and audit records; operation runner with idempotency keys scoped by owner and type, durable step records, and per-environment serialisation; tested against a fake worker | Unit tests: a retried key returns the same operation; a reused key with different parameters fails; conflicting mutations serialise; a runner restarted mid-operation resumes from its last completed step |
| M2. Worker | Disk creation (sparse ext4 seeded with the owner's SSH key), tap and nftables setup, Firecracker launch and clean stop through systemd, vsock readiness, inventory, and idempotent per-resource create and remove calls | Integration test on the droplet: launch and stop the Gate C image twice; repeated calls change nothing; inventory matches reality; no resources remain after removal |
| M3. API | Bootstrap command issuing a one-time token; token hashes; routes for capabilities, image registration, environments, operations, and events; capacity checks; audit records | API tests: unauthenticated requests rejected; another owner's environment invisible; over-capacity create rejected with a structured error; create returns `202` with operation and environment IDs |
| M4. Recovery | Startup reconciliation in the API comparing intent with worker inventory; worker restart rediscovers units; host reboot marks environments stopped; interrupted deletes resume | Scenarios R1–R5 below pass |
| M5. Client | `boxfacectl login`, `image import`, `create`, `list`, `inspect`, `start`, `stop`, `delete [--purge-data]`, with JSON output and operation following | Every lifecycle action works from an arm64 Linux client; Ctrl-C while following does not cancel the operation |
| M6. Acceptance | Run all scenarios on the droplet; repeat on the ThinkCentre when available | Results recorded in `docs/research/2026-MM-lifecycle.md` |

## Acceptance Scenarios

| ID | Scenario | Pass |
|---|---|---|
| L1 | Create with an idempotency key, then retry the same request | Same operation and environment; one disk, one tap, one VM |
| L2 | Reuse the key with different parameters | Structured conflict error; nothing created |
| L3 | Stop, start, stop | Data on the disk survives; the guest reaches readiness each start |
| L4 | Concurrent start and stop of one environment | Operations serialise; final state matches the last accepted request |
| L5 | Delete, then delete with purge on another environment | Delete keeps the disk visible in inventory; purge removes only that environment's disk |
| L6 | Create beyond configured capacity | Rejected before any resource is created |
| R1 | Kill the API after the disk is created and before launch | On restart the operation resumes; one disk; environment running |
| R2 | Kill the worker while environments run | VMs keep running; on restart the worker reports them; no duplicate launch |
| R3 | Reboot the host | Environments report stopped; start works; data intact; no session claimed live |
| R4 | Corrupt the image so launch fails after disk creation | Disk retained; tap and unit removed; environment reconciles to stopped with an actionable error |
| R5 | Kill the worker during delete | Delete resumes and removes only owned resources |
| S1 | From a guest, reach the host, the API, another guest, and the internet | Only the internet is reachable |
| S2 | Inspect audit records after L1 and L5 | Create and delete recorded without secret values |

## Measurements

Create to ready, start to ready, stop duration, API restart to reconciled, and worker restart to rediscovered, on the droplet and later on the ThinkCentre. Startup targets from the [Host Worker](../designs/host-worker.md#capacity) design are checked only on the ThinkCentre.

## Verification

- All scenarios pass, or failures produce recorded design decisions.
- `task build test lint` passes.
- [Host Worker](../designs/host-worker.md), [Operations](../designs/operations.md), and [Boxface API](../designs/api.md) record the decisions above.
- Exit: the roadmap's success signal holds under repeated operations and management-service restarts.
