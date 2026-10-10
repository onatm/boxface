# Boxface

Boxface runs persistent development environments inside Firecracker microVMs on a Linux server you control. A lightweight client, such as a Linux tablet, creates an environment, connects to it over SSH, and runs coding-agent CLIs there. The environment keeps running when the client disconnects.

Each environment has a read-only managed baseline (Debian, rootless Docker, and a Nix-built toolset with Claude Code, Codex, OpenCode, Pi, mise, Node, and Rust) and its own persistent data disk for your home directory and workspace.

## Status

Early and single-owner. What works today:

- `boxface-api` and `boxface-worker` run on one KVM host as systemd services.
- Environments can be created, listed, inspected, started, stopped, deleted, and purged through the API or `boxfacectl`, and survive API, worker, and host restarts ([acceptance results](docs/research/2026-10-lifecycle.md)).
- Guests reach the internet, but not the host, the API, each other, the LAN, or the tailnet.

Not yet built: an SSH gateway and session management, connection discovery, credential delivery to sessions, image builds through the API, backups, upgrades, and a web UI. See the [Roadmap](docs/ROADMAP.md). Everything has been tested on a nested-virtualization cloud droplet only, not yet on bare metal.

## Requirements

**Host**
- Linux on x86-64 with KVM (`/dev/kvm`). Tested on Ubuntu 24.04 with systemd 255.
- Root access, `nftables`, `iproute2`, `e2fsprogs`, `curl`, and `python3`.
- Firecracker at `/usr/local/bin/firecracker`. `experiments/gate-a/00-host-setup.sh` installs a pinned, checksum-verified v1.17.0.
- Disk space for images (about 7 GiB each) and data disks (sparse, 20 GiB by default).

**Build machine**
- Go 1.25 or newer.
- Optionally [Task](https://taskfile.dev).

**Client**
- `boxfacectl` for Linux (amd64 or arm64) or macOS, and SSH.

## Build

```sh
task build
```

This writes static binaries to `bin/`:

- `bin/linux-amd64/`: `boxface-api`, `boxface-worker`, and `boxfacectl`
- `bin/linux-arm64/`: `boxfacectl`
- `bin/darwin-arm64/` (Apple silicon) and `bin/darwin-amd64/` (Intel): `boxfacectl` for macOS

Without Task:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux-amd64/ ./cmd/...
go build -o boxfacectl ./cmd/boxfacectl   # for the machine you're on
```

## Build a guest image

Images are currently built by the experiment scripts on the host, as root. The scripts install their build dependencies with `apt`, so they need a Debian or Ubuntu host. Copy the repository to the host first, then:

```sh
experiments/gate-a/00-host-setup.sh    # Firecracker; checks KVM
experiments/gate-a/01-kernel.sh        # guest kernel (about 25 minutes on 2 vCPUs)
experiments/gate-c/01-profile.sh       # installs Nix on the host, builds the user toolset
experiments/gate-c/02-image.sh         # builds the read-only root filesystem
```

The result is `/var/tmp/boxface-gate-c/out/vmlinux` and `/var/tmp/boxface-gate-c/out/rootfs.ext4`. The toolset is defined in `experiments/gate-c/profile/flake.nix`, and the personal configuration applied on first boot is in `experiments/gate-c/dotfiles/`. No credentials are built into the image.

## Install on the host

Run these as root on the host.

1. Copy the repository and `bin/linux-amd64/` to the host, then install and start the services from the binaries directory:

   ```sh
   deploy/install.sh bin/linux-amd64
   ```

   This creates the `boxface` and `boxface-vm` system users and `/var/lib/boxface`. It then installs both services, enables them and starts them. The API listens on `127.0.0.1:7443`.

2. Create a one-time login code, valid for 15 minutes:

   ```sh
   runuser -u boxface -- boxface-api bootstrap -owner <your-name>
   ```

3. Register the image. Move it into the import directory, then register it from the client, as shown in the next section:

   ```sh
   mkdir /var/lib/boxface/import/base
   cp /var/tmp/boxface-gate-c/out/{vmlinux,rootfs.ext4} /var/lib/boxface/import/base/
   chown -R boxface:boxface /var/lib/boxface/import/base
   ```

## Use

The API listens on localhost only, so reach it through an SSH tunnel to the host:

```sh
ssh -f -N -L 7443:127.0.0.1:7443 root@<host>
```

Log in with the code from step 3, and register the image staged in step 4:

```sh
boxfacectl login http://127.0.0.1:7443                   # prompts for the code
boxfacectl image import base --alias main                 # hashes and registers it as the default image
boxfacectl image list
```

`login` also creates an SSH key for this device (`id_ed25519` next to the login file) and registers its public key with the server. New environments accept the keys registered when they are created.

Manage environments:

```sh
boxfacectl create travel                  # main image, 2 vCPUs, 2048 MiB, 20 GiB disk
boxfacectl create big --vcpus 4 --memory-mib 8192 --image <image-id-or-alias>
boxfacectl list
boxfacectl inspect travel
boxfacectl stop travel
boxfacectl start travel
boxfacectl delete travel                  # asks you to type the name; keeps the data disk
boxfacectl delete travel --purge-data     # also deletes the data disk, even after a delete
```

`create`, `start`, `stop`, and `delete` show each step until the operation finishes. Press Ctrl-C to stop watching: the operation keeps running on the server, and `boxfacectl operation <id> --follow` picks it up again. Add `--no-wait` to return immediately, `--yes` to skip the delete confirmation, and `--json` for machine-readable output.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Error, or the operation failed |
| 2 | Usage error |
| 130 | Interrupted |

The login is stored in `$BOXFACE_CONFIG`, or in `boxface/config.json` under your user config directory, with mode 0600.

### Connect to an environment

Connection discovery isn't built yet. Each environment gets its own `/30` network from `172.20.0.0/16`, and the guest is the second address in it. To find it, look on the host for the network device named after the environment ID. For example, environment `5eb2deb9-5b8b-…` uses device `bfx5eb2deb95b8b`:

```sh
ip -4 -br addr show dev bfx<first 12 hex digits of the ID>   # e.g. 172.20.0.1/30, so the guest is 172.20.0.2
```

Then connect as `dev`, through the host, with the device key `login` created (on macOS it is in `~/Library/Application Support/boxface/`):

```sh
ssh -i ~/.config/boxface/id_ed25519 -J root@<host> dev@172.20.0.2
```

Inside, `/home/dev` and `/workspace` are on the persistent data disk, and `/tmp` and `/run` are in memory. The root filesystem is read-only, there is no sudo, and Docker runs rootless as `dev`.

### Behaviour to know

- **Stopping and starting:** stopping shuts the guest down cleanly and keeps its disk. Starting boots the same disk again.
- **After a host reboot,** environments are reported `stopped` with an explanation, and are never restarted automatically.
- **If a start fails,** the environment is left `stopped` with the error. Its disk is kept.
- **Limits:** the API allows at most 4 environments by default. Change this by overriding the service command (`systemctl edit boxface-api`) to pass `-max-environments`, `-max-vcpus`, `-max-memory-mib`, or `-max-disk-gib`.
- **Deleted environments** with a kept disk stay listed until purged.
- **Tokens:** `runuser -u boxface -- boxface-api revoke -owner <your-name>` revokes all of your tokens.

## Operate the host

| What | Where |
|---|---|
| Service logs | `journalctl -u boxface-api -u boxface-worker` |
| A VM's console | `journalctl -u boxface-vm-<environment-id>` |
| Database | `/var/lib/boxface/api/boxface.db` (SQLite, owned by `boxface`) |
| Images | `/var/lib/boxface/images/<image-id>/` |
| Data disks | `/var/lib/boxface/disks/<disk-id>.ext4` |
| Runtime state | `/var/lib/boxface/environments/<environment-id>/` |
| Firewall | nftables table `inet boxface` |

## Development

```sh
task test          # unit tests
task lint          # gofmt and go vet
task integration   # builds the worker integration test binary
```

Run the worker integration test as root on a KVM host:

```sh
BOXFACE_IT_IMAGE=/var/tmp/boxface-gate-c/out ./worker.integration.test -test.v
```

Acceptance scenarios run as root on a host with the services installed:

```sh
BOXFACE_TOKEN=<token> BOXFACE_IMAGE=<image-id> test/acceptance/lifecycle.sh
BOXFACE_TOKEN=<token> BOXFACE_IMAGE=<image-id> BOXFACE_KERNEL=/var/tmp/boxface-gate-c/out/vmlinux test/acceptance/recovery.sh
```

`AGENTS.md` describes the project conventions.

## Documentation

- [Architecture](docs/ARCHITECTURE.md): system design and decisions
- [Roadmap](docs/ROADMAP.md): what's next
- [Design docs](docs/designs/): API, host worker, operations, guest, CLI, and more
- [Plans](docs/plans/) and [research results](docs/research/): the baseline experiment (Gates A–C) and the lifecycle stage
