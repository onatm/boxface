# Durable Server Lifecycle: Acceptance

**Date:** 2026-10-10

**Result:** Pass on the test droplet; ThinkCentre repeat pending

**Plan:** [Durable Server Lifecycle](../plans/2026-10-durable-lifecycle.md#acceptance-scenarios)

**Scripts:** `test/acceptance/lifecycle.sh` (L1–L6, S1, S2), `test/acceptance/recovery.sh` (R1, R2, R4, R5); R3 by hand with a host reboot

---

## Summary

All 13 acceptance scenarios pass against `boxface-api` and `boxface-worker` installed as systemd services with `deploy/install.sh`. Lifecycle covers 47 checks and recovery 27; R3 was run with three real reboots. Environments are created, listed, started, stopped, deleted, and purged through the API. Retries return the same operation, and API, worker, and host restarts never duplicate a launch, a disk, or a network device. Guests reach the internet but not the host, the API, or each other.

Running the scenarios found one ordering bug, fixed before this result: after a reboot the API reconciled before the worker's socket existed. The roadmap stage cannot exit until the same run is repeated on the ThinkCentre without nested virtualization.

## Environment

| Item | Value |
|---|---|
| Host | DigitalOcean Basic droplet, 2 vCPU, 3.8 GiB RAM, nested KVM, Ubuntu 24.04.5 LTS, kernel 6.8 |
| VMM | Firecracker v1.17.0 in systemd transient units as `boxface-vm` |
| Image | Gate C image (Linux 6.18.55 guest kernel, read-only Debian 13 root, Home Manager profile), 6.7 GiB root filesystem |
| Services | `boxface-api` (as `boxface`, `127.0.0.1:7443`, capacity 4 environments) and `boxface-worker` (root), both from the lifecycle branches |
| Guests | 1 vCPU, 512–1024 MiB, 4 GiB data disk, unless noted |

## Results

| ID | Scenario | Result |
|---|---|---|
| L1 | Create with an idempotency key, then retry | Pass: same operation and environment; one disk, one tap, one VM |
| L2 | Reuse the key with different parameters | Pass: `409 idempotency_conflict`; nothing created |
| L3 | Stop, start, stop | Pass: data written in the guest survived; readiness reached on start |
| L4 | Start and stop submitted back to back | Pass: run in order; start reached readiness; final state `stopped`/`stopped` |
| L5 | Delete one environment, purge another | Pass: deleted disk retained and still listed; purged disk removed and unlisted; other disks untouched |
| L6 | Create beyond capacity | Pass: `409 capacity_exceeded` ("environments would be 5, limit 4"); no disk created |
| R1 | Kill the API right after `create_disk` | Pass: create resumed and succeeded; one VM, one disk |
| R2 | Kill, then stop, the worker while environments run | Pass: VMs kept running; a stop waited while the worker was stopped and completed when it returned |
| R3 | Reboot the host | Pass after the fix below: environment reported `stopped`/`stopped` with an explanation before the first request; nothing left behind; started again with data intact |
| R4 | Corrupt root filesystem | Pass: create failed with `guest kernel panic: VFS: Unable to mount root fs on "/dev/vda"`; environment `stopped`; disk retained; unit, tap, and directory removed |
| R5 | Kill the worker during delete | Pass: delete resumed; disk retained; another environment's disk and state untouched |
| S1 | Guest network reach | Pass: internet reachable; host SSH, the API port, the host's public address, and another guest dropped (probe timed out) |
| S2 | Audit records | Pass: one `environment.create` despite the retry; create and delete recorded for deleted and purged environments; the API token does not appear in the database |

The client side was checked separately: the macOS build of `boxfacectl` drove the droplet's API over an SSH tunnel, including an interrupted `stop` that completed on the server and was followed again ([PR #7](https://github.com/onatm/boxface/pull/7)).

## Measurements

| Measurement | Value |
|---|---|
| Create to ready (API) | 7.1–8.5 s |
| Create to ready (`boxfacectl` over an SSH tunnel) | 8.1 s |
| Start to ready | 7.6–8.1 s |
| Clean stop | 2.3–2.6 s |
| API kill to interrupted create finished | 7.5–8.6 s |
| API restart to serving, reconciliation included | 0.13 s |
| Worker restart to running VMs rediscovered | 37–43 ms |
| Host boot to API serving, reconciled | about 12 s |
| Image registration (hashing the 6.7 GiB root filesystem) | 38 s |

These are nested-virtualization timings. The 15-second warm-start target in the [Host Worker](../designs/host-worker.md#capacity) design is met here but is checked only on the ThinkCentre.

## Findings

1. **The worker reported started before it could serve.** With `Type=simple`, systemd started the API 7 ms after the worker process; the API's startup reconciliation failed on the missing socket, and environments were reported `running` for 30 seconds after a reboot until the next reconcile. The worker now notifies systemd (`Type=notify`) after its socket is listening, and the rerun reconciled before the API served its first request.
2. **Reconciliation does not wait for the worker.** If the worker is unavailable at API startup for any other reason, stale states persist until the next reconcile, at most 30 seconds later; operations themselves retry and are unaffected.
3. **Images cannot be removed.** The corrupt test images from R4 remain registered as `ready`; there is no image delete or status change.
4. **Image registration is synchronous.** Hashing 6.7 GiB holds the request for 38 seconds; larger images may need it to become an operation.
5. **An earlier report miscounted.** The M4 pull request first reported 31 recovery checks; the script has 27, all passing.

## Not Covered

- The ThinkCentre: bare-metal timings, capacity, and Firecracker without nested virtualization.
- `boxfacectl` on arm64 Linux hardware; the build compiles.
- Concurrent creates from several clients, and capacity under memory pressure.
- Disk-full handling and image garbage collection.
- API exposure beyond localhost and TLS, which belong to the access stage.
