# Outpost user guide

The full reference for running Outpost, split by topic. New here? Start with the
[README](../README.md) for the short version.

| Page | What is in it |
| --- | --- |
| [Installing and first run](install.md) | Installing the controller and nodes, the tray icon, the first run |
| [Using tasks](using-tasks.md) | The web UI, working with tasks, agent mode, saved and scheduled tasks, projects, attachments and git |
| [Nodes working together](cluster.md) | Peer review, benchmarks and routing, best of N, memory pooling, labels, working hours, Wake-on-LAN |
| [Chat, the API and cloud planners](chat-and-api.md) | Chat, the OpenAI-compatible API, Claude and OpenRouter, the spending cap |
| [Image generation](images-generation.md) | stable-diffusion.cpp on nodes, the image models, results and their retention, the API |
| [Updating, security and operations](operations.md) | Updates and rollback, passwords, sign-in security, backups, notifications, where files live, logs, diagnostics |
| [Known limits](known-limits.md) | What is not done yet and what is untested |

**More documentation:** [SECURITY.md](../SECURITY.md) (threat model, protections, known limits, reporting) ·
[BUILDING.md](BUILDING.md) (verify downloads, build from source) ·
[ARCHITECTURE.md](ARCHITECTURE.md) (data folders, connection, task lifecycle) ·
[ROADMAP.md](ROADMAP.md) (what was built and what is untested) · [testing/README.md](../testing/README.md)

## License

Outpost is free software under the GNU Affero General Public License v3.0 only (SPDX:
`AGPL-3.0-only`); the full text is in `LICENSE`. Copyright (C) 2026 Mark (64fedoras). Because the web UI
is a network service, section 13 applies: the sidebar and the sign-in page link to the source code
(the tag of the running version) and to `/licenses`, which shows the licence and the third-party notices.
`outpost licenses` prints the same text. Licences of everything Outpost vendors, bundles or downloads are in
`THIRD_PARTY_NOTICES.txt` (regenerate with `sh scripts/gen_notices.sh` after `go mod vendor`); `LICENSE` and the
notices are also in the release zip. This is not legal advice.

## Third-party code inside the binary

Linux x86-64 builds include `libgomp.so.1` from GCC (GPLv3 with the GCC Runtime Library Exception),
unmodified, from Debian's `libgomp1` package; its license is in `bundled/LICENSE-libgomp.txt` (also in `THIRD_PARTY_NOTICES.txt`) and its
source is available from Debian (`apt source gcc`). Outpost writes it to disk only on machines that
lack it.

## Building and verifying

Release binaries are unsigned; how to check their SHA-256 hashes, how to build from source
(`sh build.sh` or `.\build.ps1`, Go only, no network needed) and how to install your own build are in
[docs/BUILDING.md](BUILDING.md).
