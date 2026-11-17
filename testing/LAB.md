# Lab checks on your own network: a from-scratch guide

This sets up an automatic check of a **real Outpost cluster** on a private network, with no cloud service involved.
Every N hours, or when you press a button, a small runner machine on your network asks your controller to run
**Check cluster** on every online node, lists the models and sends one tiny chat message. If anything fails you see
which node and which step. It does not need to know how many nodes you have, what they run on or how your network
is laid out. All addresses below (`192.0.2.x`, `gitea.example.local`) are placeholders: use your own.

**Status: not yet followed end to end by anyone but the author, and the Gitea screens are described from memory of
Gitea 1.2x.** Menu names may differ a little on your version; the ideas do not. If a step is wrong, please fix this page.

## What you need
- A running Outpost controller and at least one node (see `docs/install.md`).
- A Gitea instance you control, reachable from the runner machine. It can be new (Gitea's own manual covers
  installing it). Forgejo should work for the Gitea parts but is untested here, and its runner is a different program,
  so `scripts/lab-runner-setup.sh` does not set it up.
- One extra machine to be the **runner**: Debian or Ubuntu (it needs `apt`), x86-64 or arm64, with network access to
  your Gitea, to your controller (port 8443) and to the internet during setup (to download the runner and Node.js).
  A small VM is plenty. Ideally it is not a machine you care about, because jobs run on it **without isolation**.
- The runner must be able to open TCP connections to the controller's port (default 8443), and the **controller**
  must be able to reach each node's port 8444 (that is what Check cluster tests). They may be on different subnets.

## 1. Mirror the repository into Gitea
The lab workflow lives in this repository (`.gitea/workflows/lab.yml`). Gitea needs a copy of it:
1. In Gitea: **+ → New Migration → GitHub** (or **Git**), clone address `https://github.com/64fedoras/outpost.git`
   (or your own fork's address), tick **This repository will be a mirror**, and create it.
2. Open the mirror's **Settings → Repository → Mirror Settings** and set the **sync interval**, for example `10m`
   (Gitea has a lowest allowed value; use the lowest it accepts). Syncing is one small fetch and never starts a run.
3. Make sure Actions are on: for the instance (`[actions] ENABLED = true` in `app.ini` on versions where it is not the
   default) and for the repository (**Settings → Repository → Enable Repository Actions**).

## 2. Set up the runner machine
1. In the mirror: **Settings → Actions → Runners → Create new runner**, and copy the registration token. Save it in a
   file on the runner machine, for example `~/token` (mode 600).
2. On the runner machine, get `scripts/lab-runner-setup.sh` (clone the repository or download that one file) and read it.
3. Run it (see `--help` for all options):
   `sudo sh scripts/lab-runner-setup.sh --instance https://gitea.example.local --token-file ~/token`
   It sets the hostname to `gitea-runner` (`--keep-hostname` to leave it), installs git, Python 3 and Node.js, installs
   the gitea-runner program (checksum checked), creates a `gitea-runner` user and a systemd service, and registers with the
   single label `lab-runner:host`. The token is never stored. Running it again skips what is done.
   Behind a proxy use `sudo -E`. Use `http://` in `--instance` only on a network you trust.
4. After a few seconds the runner shows as **Idle** under **Settings → Actions → Runners**.
**The script is untested on a real machine** (it was syntax-checked only). If the download or the config step fails,
`--version` and `GITEA_RUNNER_BASE_URL` change where it downloads from.

## 3. Add the secrets
In the mirror: **Settings → Actions → Secrets** (no value goes into the repository):
- `OUTPOST_LAB_URL`: your controller **with `https://`**, for example `https://192.0.2.10:8443`. The controller only
  answers HTTPS; plain `http://` returns a bare 400 and no scheme at all fails with "unknown url type".
- `OUTPOST_LAB_TOKEN`: in the controller's web UI, **Settings → API tokens**, create one (not "chat only") and copy it.
- `OUTPOST_LAB_FINGERPRINT` (optional): the controller's certificate is self-signed, so by default it is not verified.
  The first run prints a "certificate not verified" line with the fingerprint; save that value here to pin it.
  A fingerprint is public, unlike the token.
Optional: `OUTPOST_LAB_REQUIRE` (comma-separated node names that must be online) and `OUTPOST_LAB_SKIP_CHAT=1`
can be added to the workflow's `env:` if you want them; see the top of `testing/lab_smoke.py`.

## 4. Keep Gitea from running the GitHub-only workflows
Gitea also lists the mirror's `.github/workflows/` files. They ask for runners labelled `ubuntu-24.04`, which yours
does not have, so they just queue. **Never give your runner that label**, or it would try to run the release build.
To tidy the list, open the mirror's **Actions** tab, cancel queued runs, and disable those workflows from the **⋯** menu
next to each one if your version has it. Leaving them queued is harmless.

## 5. Run it
**Actions → Lab → Run workflow.** The workflow starts only by hand and on a nightly schedule (edit the `cron` line in
`.gitea/workflows/lab.yml` in your own fork to change that), never on a sync. A healthy run ends with every step `[ok]`.
You can also run the same check by hand from any machine with Python 3:
`OUTPOST_LAB_URL=https://192.0.2.10:8443 OUTPOST_LAB_TOKEN=... python3 testing/lab_smoke.py`.

## Troubleshooting
- **`unknown url type` / `400` with no body:** `OUTPOST_LAB_URL` lacks `https://` or says `http://`.
- **`Cannot find: node in PATH`:** Node.js is missing on the runner; re-run the setup script (or `sudo apt-get install nodejs`).
- **A token error (401/403) on `/api/state`:** the token is revoked, mistyped or "chat only".
- **The job never starts:** no runner is Idle with the label `lab-runner`, Actions are off for the repository, or the
  workflow file is not in the mirror yet (sync it).
- **One node reports a port 8444 timeout in Check cluster.** The runner and the lab are fine; that node's firewall (or
  a router between subnets) blocks the **controller** from reaching it. Test from the controller's machine, not only from
  the runner: `nc -vz -w 5 NODE-ADDRESS 8444`. Do not trust `ping`; Windows and many firewalls drop it.
  - **Windows:** the network must be **Private** (`Get-NetConnectionProfile`). Outpost's installer opens the port only when
    run as administrator, and only for the local subnet. In an Administrator PowerShell, allow the range your controller
    is in (`192.0.2.0/24` is only an example), using PowerShell's own command (a quoted `netsh` name can fail silently there):
    `New-NetFirewallRule -DisplayName 'Outpost model sharing' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8444 -RemoteAddress 192.0.2.0/24 -Profile Private,Domain`
    then `Get-NetFirewallRule -DisplayName 'Outpost model sharing'` should show it enabled.
  - **Linux:** `sudo ufw allow from 192.0.2.0/24 to any port 8444 proto tcp`, or with firewalld
    `sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=192.0.2.0/24 port port=8444 protocol=tcp accept' && sudo firewall-cmd --reload`.
  - **macOS:** allow incoming connections for the `outpost` program in System Settings → Network → Firewall.
  - A node and controller on different subnets also need your router or VLAN rules to permit that traffic.
- **A step fails only on one node's model call or command:** that is a real finding about that node, not about the lab setup.
