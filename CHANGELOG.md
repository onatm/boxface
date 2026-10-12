# Changelog

All notable changes to Boxface are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-10-12

The first release: persistent development environments in Firecracker microVMs on a single KVM host, for one owner.

### Added

- **Environment lifecycle.** Create, list, inspect, start, stop, delete, and purge environments through the API and `boxfacectl`. Every change runs as a durable operation with step-by-step progress that survives API, worker, and host restarts without duplicate launches, disks, or network devices.
- **Managed baseline.** Each environment boots a read-only Debian root filesystem with rootless Docker, and keeps `/home/dev` and `/workspace` on its own persistent data disk.
- **Isolation.** Guests reach the internet, but not the host, the API, other guests, the LAN, or the tailnet. Only the host worker performs privileged operations, from validated specifications.
- **Connect.** `boxfacectl connect` reaches an environment's SSH through the API, with the host key pinned at creation, and attaches to a named tmux session that keeps running when the client disconnects. Port forwarding with `--forward`, and automatic forwarding for ChatGPT sign-in callbacks.
- **Access.** Owner login with one-time codes or a bound Tailscale identity, per-device tokens and SSH keys, and login rate limiting. The API is reachable through Tailscale Serve, public TLS with Let's Encrypt or your own certificate, or an SSH tunnel.
- **Web UI.** A setup checklist, then images and the `main` alias, profiles, default sizes, SSH keys, client logins, credentials, disk usage, and the environment list.
- **Profiles.** Toolsets built from Nix packages or a Home Manager module in an isolated builder VM, attached to environments as a read-only drive and versioned by revision.
- **Agent credentials.** Tokens and API keys stored once, encrypted with a local key or a KMS (AWS, Google Cloud, Azure, Vault, OpenBao), and delivered at run time to the commands you name. Nothing is written to the environment's disk.
- **Coding agents.** Claude Code, Codex, OpenCode, and Pi, each validated with subscription sign-in inside an environment.
- **Distribution.** Prebuilt `boxface-api`, `boxface-worker`, and `boxfacectl` for Linux x86-64, and `boxfacectl` for Linux arm64 and macOS, with SHA-256 checksums.

[0.1.0]: https://github.com/onatm/boxface/releases/tag/v0.1.0
