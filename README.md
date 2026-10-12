# Boxface

[![CI](https://github.com/onatm/boxface/actions/workflows/ci.yml/badge.svg)](https://github.com/onatm/boxface/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/onatm/boxface)](https://github.com/onatm/boxface/releases/latest)

Boxface runs persistent development environments inside Firecracker microVMs on a Linux server you control. A lightweight client creates an environment, connects to it over SSH, and runs coding-agent CLIs there. The environment keeps running when the client disconnects.

Each environment has a read-only managed baseline (Debian, rootless Docker, and a Nix-built toolset with Claude Code, Codex, OpenCode, Pi, mise, Node, and Rust) and its own persistent data disk for your home directory and workspace. The baseline is read-only to the environment, not to you: you choose its packages, tools, and configuration, and [rebuild it](#customise-the-baseline) when you want to change them.

## Features

- **Durable lifecycle.** Create, start, stop, delete, and purge environments with `boxfacectl` or the API. Operations survive API, worker, and host restarts without duplicating or orphaning VMs, disks, or network devices.
- **Persistent sessions.** `boxfacectl connect` attaches to a named tmux session over SSH tunnelled through the API, with the host key pinned at creation. Disconnects leave everything running.
- **Isolated guests.** Guests reach the internet, but not the host, the API, each other, the LAN, or the tailnet. Only the host worker performs privileged operations.
- **Versioned toolsets.** Profiles are built from Nix packages or a Home Manager module in an isolated builder VM and attached read-only, so the baseline stays fixed while your data persists.
- **Agent credentials.** Store tokens once, encrypted with a local key or a KMS, and deliver them at run time only to the commands you name.
- **Web UI.** Grouped into Environments (environments, the default template, credentials), Baseline (images, profiles), Access (devices and their SSH keys), and Host (disk, limits), with an overview that walks you through setup.
- **Private or public access.** Reach the API through Tailscale Serve, public TLS with Let's Encrypt, or an SSH tunnel.

Boxface 0.1.0 targets a single owner on one KVM host. Image builds through the API, backups, upgrades, and environment actions in the web UI are planned; see the [Roadmap](docs/ROADMAP.md) and the [Changelog](CHANGELOG.md).

## Requirements

**Host**
- Linux on x86-64 with KVM (`/dev/kvm`). Tested on Ubuntu 24.04 with systemd 255.
- Root access, `nftables`, `iproute2`, `e2fsprogs`, `curl`, and `python3`.
- Firecracker at `/usr/local/bin/firecracker`. `images/host-setup.sh` installs a pinned, checksum-verified v1.17.0.
- Disk space for images (about 7 GiB each) and data disks (sparse, 20 GiB by default).

**Build machine**
- Go 1.26 or newer.
- Optionally [Task](https://taskfile.dev).

**Client**
- `boxfacectl` for Linux (amd64 or arm64) or macOS, and SSH.

## Download

Each [release](https://github.com/onatm/boxface/releases) has an archive per platform and a `SHA256SUMS` file:

| Archive | Contents |
|---|---|
| `boxface_<version>_linux_amd64.tar.gz` | `boxface-api`, `boxface-worker`, `boxface-run`, `boxfacectl`, and `deploy/` for the host |
| `boxface_<version>_linux_arm64.tar.gz` | `boxfacectl` |
| `boxface_<version>_darwin_arm64.tar.gz`, `boxface_<version>_darwin_amd64.tar.gz` | `boxfacectl` for macOS |

```sh
curl -LO https://github.com/onatm/boxface/releases/download/v0.1.0/boxface_0.1.0_linux_amd64.tar.gz
curl -LO https://github.com/onatm/boxface/releases/download/v0.1.0/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf boxface_0.1.0_linux_amd64.tar.gz
```

## Build from source

```sh
task build
```

This writes static binaries to `bin/`:

- `bin/linux-amd64/`: `boxface-api`, `boxface-worker`, `boxface-run` (the guest credential launcher built into images), and `boxfacectl`
- `bin/linux-arm64/`: `boxfacectl`
- `bin/darwin-arm64/` (Apple silicon) and `bin/darwin-amd64/` (Intel): `boxfacectl` for macOS

Without Task:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux-amd64/ ./cmd/...
go build -o boxfacectl ./cmd/boxfacectl   # for the machine you're on
```

## Build a guest image

Images are built on the host, as root, by the scripts in `images/`. They install their build dependencies with `apt`, so they need a Debian or Ubuntu host. Copy the repository to the host first, with `boxface-run` at `bin/linux-amd64/boxface-run` (from `task build` or the release archive), then:

```sh
images/host-setup.sh    # Firecracker; checks KVM
images/kernel.sh        # guest kernel (about 25 minutes on 2 vCPUs)
images/profile.sh       # installs Nix on the host, builds the toolset
images/image.sh         # builds the read-only root filesystem with the toolset
```

The result is `/var/tmp/boxface-images/main/out/vmlinux` and `/var/tmp/boxface-images/main/out/rootfs.ext4`. No credentials, host keys, or machine IDs are built into the image; the build fails if it finds any.

For [profiles](#profiles), build a base image without a toolset and the builder image instead, with `images/profile-images.sh` after `kernel.sh` and `profile.sh`. The results are `vmlinux`, `base.ext4`, and `builder.ext4` in `/var/tmp/boxface-images/profiles/out/`.

### Customise the baseline

The baseline is read-only inside environments, so nothing an agent or workload does can change it, but it is yours to define. Edit these and rebuild the image:

| What | Where |
|---|---|
| System packages and services (Debian, Docker, SSH) | `images/system/` ([mkosi](https://github.com/systemd/mkosi) configuration and files) |
| Guest integration: PATH, credential launchers, first-boot setup | `images/guest/` |
| Toolset: agents, languages, CLIs | `images/profile/flake.nix` (Nix packages or a Home Manager module) |
| Personal configuration applied on first boot | `images/dotfiles/` ([chezmoi](https://www.chezmoi.io) source) |
| Guest kernel options | `images/kernel.fragment` |

Keep the `boxface-*` units, the `dev` user, the `sshd` configuration, and the mounts in `images/system/`: they are how Boxface boots, reaches, and connects to an environment. The [image contract](docs/designs/guest.md#image-contract) lists what an image must provide, and `images/rootfs.sh` fails the build when an image breaks it, so a broken image fails at build time instead of booting and never becoming ready.

Register the rebuilt image and point `main` at it; new environments use it, and existing environments keep the image they were created with. With profiles, change the toolset on the web UI's Profiles page instead: no image rebuild is needed.

## Install on the host

Run these as root on the host.

1. Install and start the services from the extracted release archive:

   ```sh
   boxface_0.1.0_linux_amd64/deploy/install.sh boxface_0.1.0_linux_amd64
   ```

   From a source build, run `deploy/install.sh bin/linux-amd64` in the repository instead.

   This creates the `boxface` and `boxface-vm` system users and `/var/lib/boxface`. It then installs both services, enables them and starts them. The API listens on `127.0.0.1:7443`. On the first install it also creates your owner (`$BOXFACE_OWNER`, else the `sudo` user) and prints a setup URL with a one-time code, valid for 15 minutes. If it expires, create another code with `runuser -u boxface -- boxface-api bootstrap -owner <your-name>` and open `/login?code=<code>`.

2. Stage the image in the import directory:

   ```sh
   mkdir /var/lib/boxface/import/base
   cp /var/tmp/boxface-images/main/out/{vmlinux,rootfs.ext4} /var/lib/boxface/import/base/
   chown -R boxface:boxface /var/lib/boxface/import/base
   ```

3. Expose the API (next section), then open the setup URL. The Overview page walks through registering the staged image as `main`, building a profile, reviewing the default template, and logging in each client device with a code from the Devices page, which also registers the device's SSH key.

## Expose the API

The API listens on `127.0.0.1:7443` only. Choose how clients reach it, and put the flags in a service override with `systemctl edit boxface-api`.

**Tailscale**, for a host whose public address is disabled:

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/boxface-api serve -tailscale-socket /var/lib/boxface/api/tailscale.sock
```

Then publish the socket to your tailnet. The first time, Tailscale prints a link to enable Serve and HTTPS certificates for the tailnet:

```sh
tailscale serve --bg unix:/var/lib/boxface/api/tailscale.sock
```

Clients use `https://<host>.<tailnet>.ts.net`. To log in from your tailnet devices without a code, bind your Tailscale login to your owner once:

```sh
runuser -u boxface -- boxface-api bootstrap -owner <your-name> -tailscale-login <you@github>
```

`boxfacectl login https://<host>.<tailnet>.ts.net` and the web UI then sign in with no code. You can also bind your login from the web UI's Devices page when you open it through Tailscale. Only the Tailscale socket honours the login, and tagged devices still need a code.

**A public address**, with a Let's Encrypt certificate for a domain that resolves to the host and port 443 open:

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/boxface-api serve -tls-listen :443 -tls-domain boxface.example.com
AmbientCapabilities=CAP_NET_BIND_SERVICE
```

To use your own certificate instead, pass `-tls-cert FILE -tls-key FILE`; restart the service to load a renewed certificate.

**An SSH tunnel** needs no change on the host:

```sh
ssh -f -N -L 7443:127.0.0.1:7443 root@<host>
```

After 10 failed logins from one address, that address can't log in until 10 minutes after the first failure.

## Use

The web UI at the API's address (`/`) is for configuration: the default template, credentials, images, profiles, devices and SSH keys, host disk and limits, and a read-only list of environments with their `connect` commands. `boxfacectl` provisions and connects.

Log in with a code from the web UI's Devices page:

```sh
boxfacectl login https://<host>.<tailnet>.ts.net          # or https://boxface.example.com, or http://127.0.0.1:7443 over a tunnel
```

Images can also be registered from the CLI: `boxfacectl image import base --alias main`, then `boxfacectl image list`.

`login` also creates an SSH key for this device (`id_ed25519` next to the login file) and registers its public key with the server. An environment accepts the keys registered when it last started, so after logging in on a new device, restart existing environments once (`boxfacectl stop NAME`, then `connect NAME`); `connect` tells you when this is needed. Removing a key takes effect the same way.

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

```sh
boxfacectl connect travel                 # tmux session "main"; starts travel first if it is stopped
boxfacectl connect travel --session work  # another tmux session in the same environment
```

Agents sign in inside the environment, once per environment: run `claude` and `/login`, `codex login`, `opencode auth login`, or `pi` and `/login`. Open the URL it prints on your client. ChatGPT sign-ins redirect to `localhost:1455`, which `connect` forwards to the environment automatically; add `--forward PORT` to reach other guest ports, such as a dev server. Logins are stored in your home on the data disk and survive restarts.

`connect` runs your `ssh` with the device key, the environment's host key pinned in `known_hosts` beside the login file, and `boxfacectl tunnel` as the proxy command, so it works the same through a public address or Tailscale. Closing the terminal or losing the network leaves tmux and everything in it running; run `connect` again to reattach. Environments created before host keys were pinned have none and must be recreated.

The image needs `tmux` in its profile; `images/profile/flake.nix` includes it.

Inside, `/home/dev` and `/workspace` are on the persistent data disk, and `/tmp` and `/run` are in memory. The root filesystem is read-only, there is no sudo, and Docker runs rootless as `dev`.

### Profiles

A profile is the set of tools your environments get, built from Nix packages (or a Home Manager module) in an isolated builder VM and attached to environments as a read-only drive. Profiles are managed on the web UI's Profiles page or through the API (`/v1/profiles`). `boxfacectl create NAME` uses the profile named `main` once it has a ready revision, and `--profile NAME` picks another. An environment keeps the revision it was created with; `boxfacectl inspect` shows when a newer one is ready. Profiles need the base image and builder image from `images/profile-images.sh`: import `vmlinux` with `base.ext4` (as `rootfs.ext4`) as `main`, and copy `vmlinux` and `builder.ext4` (as `rootfs.ext4`) to `/var/lib/boxface/builder`.

Disk space: the web UI's Images page shows free space and what uses it. Unused profile drives are removed automatically; remove images that no environment uses, or clear the Nix build cache, to free more. Profile builds need 10 GiB free.

### Agent credentials

Save tokens and keys once on the web UI's Credentials page instead of signing in to tools in every environment. Each credential is given to the commands you name, such as `claude`, `gh`, or `terraform`, or to every session:

- **Commands:** each shell `connect` opens puts small launchers for those commands first on PATH. Running `gh` gives only `gh` (and what it starts) the value; the shell itself never holds it.
- **Every session:** the value is exported in each new shell. Every process in the session can read it, so prefer naming commands.
- **Presets:** Claude Code token (`claude setup-token` on any machine with a browser), OpenAI and Anthropic API keys, and a GitHub token for `gh`.
- **ChatGPT subscriptions** (Codex, OpenCode, Pi) cannot be shared: their tokens rotate on use. Sign in inside each environment as above.

Commands get the current value each time they start, in shells opened after it was saved; nothing is written to disk in the environment. To run a command without stored credentials, call it by its full path, for example `/opt/boxface/profile/bin/claude`. Environments need an image built after this feature.

### Credential encryption

Stored credentials are encrypted with [Go CDK `secrets`](https://gocloud.dev/howto/secrets/). The operator chooses the key with `-secrets-keeper` in the service override:

| Keeper | `-secrets-keeper` |
|---|---|
| Local key file (default) | `file:///var/lib/boxface/api/secrets.key` |
| AWS KMS | `awskms://<key-id-or-alias>?region=<region>` |
| Google Cloud KMS | `gcpkms://projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>` |
| Azure Key Vault | `azurekeyvault://<vault>.vault.azure.net/keys/<key>` |
| HashiCorp Vault or OpenBao Transit | `hashivault://<key>`, with `VAULT_SERVER_URL` and `VAULT_SERVER_TOKEN` in the service environment |

Cloud keepers read credentials the usual way for their SDK (instance roles, workload identity, or environment variables). To switch keepers, stop `boxface-api`, re-encrypt, then start it with the new URL:

```sh
runuser -u boxface -- boxface-api secrets rekey -from file:///var/lib/boxface/api/secrets.key -to 'awskms://alias/boxface?region=eu-west-1'
```

### Behaviour to know

- **Stopping and starting:** stopping shuts the guest down cleanly and keeps its disk. Starting boots the same disk again.
- **After a host reboot,** environments are reported `stopped` with an explanation, and are never restarted automatically.
- **If a start fails,** the environment is left `stopped` with the error. Its disk is kept.
- **Limits:** the API allows at most 4 environments by default. Change this by overriding the service command (`systemctl edit boxface-api`) to pass `-max-environments`, `-max-vcpus`, `-max-memory-mib`, or `-max-disk-gib`.
- **Deleted environments** with a kept disk stay listed until purged.
- **Tokens:** `runuser -u boxface -- boxface-api revoke -owner <your-name>` revokes all of your tokens and removes the SSH keys those devices registered. To revoke one device, use the web UI's Devices page.

## Operate the host

| What | Where |
|---|---|
| Service logs | `journalctl -u boxface-api -u boxface-worker` |
| A VM's console | `journalctl -u boxface-vm-<environment-id>` |
| Database | `/var/lib/boxface/api/boxface.db` (SQLite, owned by `boxface`) |
| Credential key | `/var/lib/boxface/api/secrets.key` (created on first start, mode 0600). Back it up separately from the database: without it, stored credentials cannot be decrypted and must be entered again. See [Credential encryption](#credential-encryption) to use a KMS instead |
| Images | `/var/lib/boxface/images/<image-id>/` |
| Data disks | `/var/lib/boxface/disks/<disk-id>.ext4` |
| Runtime state | `/var/lib/boxface/environments/<environment-id>/` |
| Firewall | nftables table `inet boxface` |

## Development

```sh
task test          # unit tests
task lint          # gofmt and go vet
task integration   # builds the worker integration test binary
task dist          # release archives and SHA256SUMS in dist/
```

Run the worker integration test as root on a KVM host:

```sh
BOXFACE_IT_IMAGE=/var/tmp/boxface-images/main/out ./worker.integration.test -test.v
```

Acceptance scenarios run as root on a host with the services installed:

```sh
BOXFACE_TOKEN=<token> BOXFACE_IMAGE=<image-id> test/acceptance/lifecycle.sh
BOXFACE_TOKEN=<token> BOXFACE_IMAGE=<image-id> BOXFACE_KERNEL=/var/tmp/boxface-images/main/out/vmlinux test/acceptance/recovery.sh
```

CI runs lint, tests, and the build on every pull request. To release, add the version's section to `CHANGELOG.md`, then push a `vX.Y.Z` tag: the release workflow runs the checks, builds the archives, and publishes a GitHub release with that section as its notes.

`AGENTS.md` describes the project conventions.

## Documentation

- [Architecture](docs/ARCHITECTURE.md): system design and decisions
- [Roadmap](docs/ROADMAP.md): what's next
- [Design docs](docs/designs/): API, host worker, operations, guest, CLI, and more
- [Plans](docs/plans/) and [research results](docs/research/): implementation plans and validation results
- [Changelog](CHANGELOG.md): what changed in each release
