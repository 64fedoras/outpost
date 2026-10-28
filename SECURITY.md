# Security

Outpost is built for a **home or lab network that you control**. This page says what it assumes,
what it does to protect you, and what it does not do. Read it before exposing Outpost to anything
other than your own LAN.

## Threat model

**Assumptions (what Outpost trusts)**

- **A trusted LAN.** Every device on the network is assumed to be yours. Outpost is not meant to be
  put on the internet, on shared Wi-Fi or on a network with guests or untrusted devices.
- **One admin.** There is a single administrator login for the web UI, chosen by you on first run.
  There are no roles, no per-user permissions and no multi-tenant separation. Anyone signed in as the admin
  (or holding one of its API tokens) can do everything the controller can do, including running commands on
  every exec-enabled node.
- **The controller is trusted.** It holds the settings, the encrypted Claude/OpenRouter keys, node keys,
  task history and releases. Whoever controls the controller machine controls the cluster.
- **Nodes are disposable, low-privilege machines.** A node should run as an ordinary user (not root or
  SYSTEM) on a machine you can wipe. Nodes keep no job data after a job; they hold models, the agent
  binary and their own join state and TLS pin. A compromised node should cost you that machine, not the
  cluster. Do not put credentials or other valuable data on a node you enable command execution on.

**In scope** (we want to hear about these)

- Bypassing admin sign-in, sessions, API tokens or the sign-in lockout.
- A node or a device without a join code getting into the cluster, or impersonating the controller
  despite certificate pinning.
- A node acting beyond its role (reading other nodes' or tasks' data, or controlling the controller).
- Cross-site scripting, request forgery and similar flaws in the web UI, or ways around the browser
  hardening below.
- Path traversal or arbitrary file write through file transfer, attachments, uploads or archives.
- A tampered update being accepted by the update checks.
- Secrets showing up in logs, backups that are not encrypted, or the first-run setup screen being
  reachable from outside the LAN default.

**Out of scope**

- Attacks that need an already-hostile LAN, such as ARP or IP spoofing, passive sniffing of the
  pooling port, or a rogue device on your network (see the pooling section below).
- Anyone who already has admin access, root on the controller, or physical access to a machine.
- Commands run by an exec-enabled node doing what they were told to do. This is the product's job;
  see "Known limits" for what is and is not isolated.
- Flaws in llama.cpp, Go, Chromium or your operating system. Report those upstream.
- Denial of service from a device that is allowed to talk to the controller, and the self-signed
  certificate warning (you can install your own certificate in Settings).

## What Outpost does today

- **TLS everywhere between nodes and controller, with fingerprint pinning.** The controller makes a
  self-signed certificate; nodes pin its SHA-256 fingerprint when they join and refuse a different one.
  Changing the certificate is a deliberate step (Settings → HTTPS certificate, `outpost repin`).
- **Single-use join codes.** Adding a node needs a join code from the web UI. Codes are single use and
  expire; generating a new code cancels the previous unused one. After joining, a node authenticates with
  its own key.
- **First-run setup limited to the LAN.** The first admin password is chosen in the browser, and by
  default only from private/LAN addresses. Outpost never generates or logs a password. To allow setup from
  elsewhere you must opt in with `--setup-allow-from` (CIDRs, or `any`).
- **Sign-in protection.** Passwords are stored as PBKDF2 hashes. Ten failed sign-ins from one address
  in 15 minutes block that address for 15 minutes. You can list and end sessions, and sign out
  everywhere. API tokens are shown once, stored hashed, and cannot manage tokens, sessions, the
  certificate or backups.
- **Browser hardening.** A strict `Content-Security-Policy` (no inline script or style), plus
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `Cross-Origin-Opener-Policy` and a restrictive `Permissions-Policy`. There is deliberately no HSTS
  because the certificate is self-signed by default.
- **Memory pooling is off by default and LAN-only.** llama.cpp's RPC protocol has no authentication or
  encryption. When you switch pooling on, the RPC server listens only on `127.0.0.1` behind a gatekeeper
  that accepts the main node's IP address only, and only while a pool is running. This is a source-IP
  check, not strong authentication, and pool traffic is clear text. Use it only on a network you trust.
  A TLS tunnel for the pool is a design note (roadmap row 24), not built.
- **Encrypted backups.** Backups are AES-256-GCM with a key derived from your passphrase (PBKDF2). The
  passphrase cannot be recovered.
- **Supply chain.** GitHub Actions in this repository are pinned to commit SHAs and kept current by
  Dependabot. Dependencies are vendored in `vendor/`. Releases are built by CI from `main`.
- **Update verification.** The controller can fetch releases from this repository's GitHub releases and
  checks each file against the release's `SHA256SUMS` and that it is an Outpost binary of the expected
  version before accepting it. **Caveat:** `SHA256SUMS` is itself unsigned and comes from the same
  place as the binaries, so it catches corrupt downloads, not a compromised GitHub account or release.
  Keep **Install them automatically** off unless that account is locked down (hardware key or passkey
  2FA, no stray write access). With it off, a person still presses the update button.

## Known limits

These are real gaps. Please do not treat them as fixed.

- **Node command execution is not sandboxed.** An exec-enabled node runs model-written commands (and
  project build and test commands) with the node's own rights in the job workspace. A bad model output, a
  poisoned dependency or a prompt injection from a web lookup could read or change anything that account can
  reach, and use the node's network access. A node installed with `sudo` may run jobs as root. Mitigations
  today: run nodes as an ordinary user, enable exec only on machines you can wipe, and keep valuables off
  them. A per-OS sandbox design (Landlock, seccomp and namespaces on Linux, job objects and restricted
  tokens on Windows, Seatbelt on macOS) is written up in `design/sandbox-exec.md` in the project files,
  and is a proposal that is not built or decided.
- **File path checks do not follow symlinks.** Paths a job or request names are checked lexically to keep them inside
  the job workspace (`..` and absolute paths are refused). A symlink created inside a workspace (for
  example by a build command) is not resolved by that check, so it can point outside the workspace.
  Archives Outpost unpacks itself skip or refuse symlinks. Resolving symlinks in the check is planned.
- **Binaries are unsigned.** Release binaries are not code-signed or notarized (Windows
  SmartScreen and Defender may complain; macOS Gatekeeper may too). Verify downloads against
  `SHA256SUMS` and see [docs/BUILDING.md](docs/BUILDING.md) to build from source.
- **Unsigned update checksums** (see above).
- **Pooling is clear text and IP-based** (see above).
- **Untested on real hardware.** Windows (service, Defender, updates), macOS, and the full 0.8.x feature set
  have not been tested on a real cluster. Treat their security behaviour as unverified too.
- **No third-party audit.** `govulncheck` has not been run as part of CI yet.

## Hardening checklist for your install

1. Keep the controller and nodes on a private network; do not forward their ports from your router.
2. Choose a strong admin password at first run, and do it right after install.
3. Run nodes as a normal user, and enable command execution only where needed.
4. Leave memory pooling off unless you need it, and firewall the pool port from everything but the pool.
5. Leave automatic update install off unless your GitHub account is locked down.
6. Take encrypted backups and keep the passphrase somewhere safe.

## Reporting a vulnerability

Please report privately, not in a public issue.

- Use **GitHub private vulnerability reporting**: on the repository, open the **Security** tab and choose
  **Report a vulnerability**.
- Direct contact: **TODO: owner contact to be filled in by Mark** (an email address or other private
  channel).

Include the version (Settings or the Updates tab), what you did, what you expected and what
happened. This is a small personal project maintained by one person, so there is no bounty or
guaranteed response time; the aim is to acknowledge reports within about a week and to fix
confirmed issues in the next release. Please give a reasonable time to fix before disclosing publicly.

## Supported versions

Only the latest release is supported. Every merge to `main` becomes a new release; update to get fixes.
