# Updating, security and operations

Updating and rollback, passwords, sign-in security, backups, notifications, where files live, logs and diagnostics.

Part of the [Outpost user guide](USER-GUIDE.md).

## Updating

Every merge to `main` is built by GitHub Actions as the next patch version (0.8.1, 0.8.2, ...; after patch 19 the minor rolls, so 0.8.19 is followed by 0.9.0) and published as a release `v<version>` with the five binaries, a zip and `SHA256SUMS`, so each build shows up as a newer version in the Updates tab. Major versions (1.0) are set by hand in `cmd/outpost/proto.go`, and versions compare numerically (0.8.10 is newer than 0.8.9) in `cmd/outpost/proto.go`.

**From GitHub (on by default).** The controller checks this repository's latest release every 6 hours
(and when you click **Check now** on the Updates tab) and downloads the binaries for its own platform
and for the platforms of the nodes it knows. Each file must match the release's `SHA256SUMS` and be an
Outpost binary of that version, or it is discarded; accepted ones show up under *Uploaded releases*
exactly like a manual upload, so the Update buttons and rollback work unchanged. `SHA256SUMS` catches
corrupt downloads but is not signed, so it does not protect against a tampered repository. Under
**Settings → Updates from GitHub** you can switch the checks off, or switch on **Install them
automatically**: idle nodes update first (busy ones are retried at the next check), then the
controller (it waits up to 10 minutes for the nodes' downloads to finish first), and only when no task is planning, running or testing. **Check and install** on the Updates
tab does the same once, on request. After a rollback, automatic install skips the version you left
(and older ones) until a newer release appears. Nothing but the version check and the downloads goes to
GitHub. This path has only been tested against a stub server, not the live GitHub API.

**Which repository, and private repositories.** *Settings → Updates from GitHub* has a **Repository to
trust for updates** field. Enter a `github.com` address such as `https://github.com/64fedoras/outpost`
(or just `owner/repo`; other hosts are refused); empty means the default. Whoever controls that
repository can ship code that runs on the controller and every node, so only enter one you own or fully
trust. For a **private** repository add an access token:

1. On GitHub: *Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate
   new token*.
2. Set *Repository access* to **Only select repositories** and pick the repository, then under
   *Repository permissions* set **Contents: Read-only** (Metadata: Read-only is added automatically).
   Choose an expiry you can live with; an expired token shows up as "GitHub rejected the access token".
3. Paste the token into **Access token for a private repository**, press **Test connection**, then **Save**.

The token is stored encrypted on the controller, is never sent to nodes, is not shown again after saving
(the page only says *token set*; **Clear** removes it) and is sent only to `api.github.com`; private
release files are downloaded through the authenticated API. Public repositories need no token. Changing
the repository clears the rollback hold. The private-repository path has only been tested against a stub
server, not a live private repository.

Or by hand:

1. **Updates tab:** drop the new binaries anywhere on the page (or click the box to choose them), one per platform you use (`outpost-linux-amd64`, …).
2. Click **Update** on the controller row (updates the web UI too; you stay signed in), then
   **Update N nodes, one first**. Each row shows the machine's status, its version and a live progress bar.

Nodes verify the download's SHA-256, run it once to confirm the version, and restart into it.
Updates need no root and survive reboots. Nodes with running jobs wait until they are idle.

**One node first.** *Update N nodes* (and automatic install from GitHub) updates a single idle node first,
waits up to 15 minutes for it to reconnect on the new version, then checks it: a short model call if it had
a model loaded (after waiting for the model to load again) and an `echo` command if commands are on. Only
then are the other nodes updated. If the first node does not come back, or fails its check, it is rolled
back to its previous version, the rest are left alone, automatic install skips that version from then on,
and an *Update failed* notification says why. A line above the table shows how far it got.

### Suggested models between releases

Each release also carries `catalog.json`, the list of suggested models (checked against `SHA256SUMS`
like the binaries). When the controller checks GitHub for updates it fetches that list from the newest
release and uses it on top of the one built into its own version: entries are updated and new models
are added, none are removed. So new good models show up on the Models page without updating Outpost.
The Updates page says which list is in use. Every entry is still checked against each node's memory
before it is suggested. Turning off GitHub checks (Settings) turns this off too.

### Rolling back

Every machine keeps the last few Outpost versions it has run in its data folder (`agent/`). On the
**Updates** tab, **Roll back to vX** restarts the controller or a node on the previous version, and it
stays there across reboots until you install an update. Each node also keeps its previous llama.cpp
build on disk.

## Passwords

- Change it in **Settings → Admin password** (8+ characters; other browsers are signed out).
- Locked out: on the controller run `sudo outpost reset-password`. It asks for a new password, or
  you leave it blank (or pass `--clear`) to remove the password so you choose a new one in the web UI
  (open it right away, as on first install). It restarts the service and signs everyone out. Outpost
  never generates a password.
- The password is stored as a PBKDF2 hash in `/var/lib/outpost/controller/admin.json` and persists
  across restarts, reboots and updates.

## Sign-in security, API tokens, certificate and backups

**Settings → Sign-in security.** After 10 failed sign-ins from one address in 15 minutes that address
is blocked for 15 minutes (the page shows the failure count). It lists every signed-in browser with
Sign out, and **Sign out everywhere**. **API tokens** are named, shown once, stored hashed, and sent
as `Authorization: Bearer opt_…` to any `/api/` endpoint a script needs (state, tasks and so on).
Tokens cannot manage tokens, sessions, the certificate or backups; only a browser sign-in can. A token
made with **Chat only** ticked works only on the OpenAI-compatible `/v1/` API (see [Chat and the API](chat-and-api.md)).

**Browser hardening.** Every controller response carries `Content-Security-Policy` (scripts, styles and
images only from the controller itself, no inline code), `X-Frame-Options: DENY`,
`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy` and a
`Permissions-Policy` that turns off camera, microphone and location. There is deliberately no
`Strict-Transport-Security`: the controller's certificate is self-signed, and HSTS would take away the
browser's "continue anyway" button after a certificate change. The web UI therefore has no `style=`
attributes or inline handlers; new UI code must use CSS classes and `addEventListener`.

**Settings → HTTPS certificate.** Paste a certificate chain and key (PEM) from your own CA or a
Let's Encrypt DNS-01 certificate. Connected nodes (0.7.0 and newer) are told the new fingerprint
first and confirm it, then the controller switches without a restart. Nodes that were offline, or
are still on an older version, keep the old pin: the page lists them, and on each run
`outpost repin --fingerprint <sha256>` (add `--data DIR` if needed). The node picks it up on its next
connection attempt. Update nodes to 0.7.0 before replacing the certificate.

**Settings → Backup.** *Download backup* gives one encrypted `.opb` file (AES-256-GCM, key from your
passphrase with PBKDF2) holding settings, the encrypted Claude key and `secret.key`, node keys, the
TLS certificate, API tokens and all tasks. Models, runtimes and releases are left out unless you tick
the box. The passphrase cannot be recovered. On a fresh machine:

```sh
sudo outpost install controller --advertise https://IP:8443 --restore outpost-backup-….opb
```

(add `--force` to overwrite an existing controller's data; set `OUTPOST_BACKUP_PASSPHRASE` to skip the
prompt). Nodes reconnect on their own because the node keys and certificate come back unchanged.
*Nightly backup* writes a copy to a folder on the controller (for example an NFS mount) about every
24 hours and keeps the last 7. Its passphrase is stored encrypted with `secret.key`. Each copy is read back
right after it is written (decrypted and unpacked in memory, nothing restored), and the Backup card says
"read back and verified (N files)" or why it failed. A failed or unreadable copy also sends a *Backup failed*
notification (once per distinct failure). Saving the schedule makes the first copy straight away.

## Notifications

**Settings → Notifications** sends a message when the planner asks a question, a plan is ready to approve,
a task finishes or fails, the spending cap pauses a task, a node has been offline for 5 minutes, a nightly
backup fails its check, the controller's disk is nearly full, a node update or rollback fails, or a model
download, copy or load fails. Each event can be switched off. The **morning summary** (off until ticked, at
08:00 controller time by default) sends one message a day listing tasks that finished or failed since the
last one, tasks waiting for you and offline nodes; on a quiet day it sends nothing. Every message links to the task (or the Nodes page).

- **Browser:** desktop notifications while an Outpost tab is open (the browser asks permission when you save).
- **ntfy:** paste the topic URL (`https://ntfy.sh/my-topic` or your own server). Add an access token only for
  protected topics. Tapping the push opens the task.
- **Webhook:** a JSON `POST` with `event`, `title`, `message`, `task_id`, `url` and `time`, plus `content`
  and `text` (the same sentence) so Discord and Slack style endpoints accept it. Home Assistant can read it with a webhook trigger.

Links use the address you were browsing when you saved or tested, or the *Address used in links* box if
you fill it in (set it if you reach the controller by a name or IP different from the one nodes use).
*Send test* checks the channels. Notifications are not stored: the controller keeps only the last 50 in memory for browsers.

## Config file, store check and node health

- **Config file** (Settings → Config file): downloads settings, saved and scheduled tasks and node labels as one
  readable JSON file with no keys, tokens or passwords. Import merges a file back in and keeps the secrets
  already on that controller. It is not a backup: tasks, models and node keys are not in it.
- **Model store check:** about five minutes after start and then weekly, the controller hashes every stored
  model file again and compares it with the checksum recorded at download. A damaged or missing file raises a
  banner and a *model download/load failed* notification; **Retry** on the Models page downloads it again.
  It finds damage after the download, not a bad download.
- **Node health:** a node's card shows *Shaky* (amber) or *Unreliable* (red) when, in the last 7 days, it
  disconnected or could not take or finish work often (1 point per disconnect, 2 per refused or lost call;
  5 points amber, 15 red; **Settings → Node health colours** changes the two numbers). Red nodes are handed work after all others. The record is `health.json` in the
  controller's data folder. A node's card has a **Reset health** button that forgets its recent events.

## Where things live

| | Controller (`/var/lib/outpost/controller`) | Node (`~/.outpost/node`) |
| --- | --- | --- |
| Config | `tls.crt/key`, `admin.json`, `api_tokens.json`, `settings.json` (secrets encrypted with `secret.key`), `nodes.json` | `node.json` (node key) |
| Models | `models/<name>/` (only for models stored on the controller) | `models/<name>/` (from Hugging Face or the controller) |
| llama.cpp | `runtime/<variant>/` (from GitHub releases) | `llama/<build>-<variant>/` |
| Work | `tasks/<id>/` (conversation, `result.zip`, `artifacts/`, attachments; `repo/` unless the task is in a project), `projects/<id>/` (`project.json`, `repo/`) | `work/` — deleted after every job |
| Updates | `releases/`, `agent/` | `agent/` |
| Logs | journald on Linux (`journalctl -u outpost-controller`) | journald on Linux; `node.log` on Windows and macOS, and when a tray started the node |

**Safe saves.** Settings, nodes, sessions, tokens, projects, model records and each task's `task.json` are written to a
temporary file, flushed to disk and renamed into place, so a power cut or a full disk leaves the old file or the new one,
never half of each. `settings.json`, `nodes.json`, `project.json`, `task.json` and the node's `node.json` also keep the
previous save as `.bak`. If a file is damaged at start-up, the previous save is loaded instead; a task that cannot be read
at all is moved to `tasks/_damaged/` rather than vanishing. Either way a notice at the top of the web UI says what happened
until you dismiss it.

**Log size.** `node.log` and `tray.log` stay under 10 MB each; the previous 10 MB is kept as `node.log.old` /
`tray.log.old`. Crash output from a node started by the tray or the macOS LaunchAgent goes to `node-console.log`.

**Stopping and updating.** A stop from systemd (or Ctrl+C), an update and a rollback first save every task, then the
controller exits. Tasks that were running resume when it is back (local planners on their own, Claude and OpenRouter
tasks with Resume, as before).

**Controller disk.** *Settings → Application → Storage* shows what uses the controller's disk (tasks,
models, releases, llama.cpp builds, projects) and how much is free. Below 10% or 5 GB free, a red line at
the top of every page says so and a *disk low* notification is sent once (it clears above 15%). Optionally,
**Clean up tasks that finished more than N days ago** removes, once a day, their per-round copies (those
rounds can then no longer be undone), screenshots and the cached result zip (rebuilt when you download it),
and, if ticked, their build outputs. Conversations, the task's files and attachments always stay, and the
task's event log notes what was removed. **Clean up now** runs it at once.

**Hung model watchdog.** While a node runs a model call, it watches llama-server: if nothing has come back
for 3 minutes and llama-server's health check then fails twice, the call is stopped, llama-server is
restarted, and the subtask goes back in the queue for another node. A model that is merely slow (a long
prompt on a CPU) keeps answering its health check and is left alone.

**Reading a node's log from the browser.** *Logs* on a node's card (Nodes → List, click the node) shows the end of
that node's Outpost log, its llama-server output or its tray log, and follows new lines while it is open. It
works on every platform, including Linux nodes whose log otherwise only goes to journald. Nodes older than
this feature cannot answer; update them first.

**Diagnostics for a bug report.** *Settings → Security and backup → Download diagnostics* builds one zip in your
browser: `summary.txt` (versions, nodes, uptime), the controller's recent log, settings, nodes and models
(every key, token, password, hash and URL replaced by `(removed)`), the event log of the 20 newest tasks and,
if ticked, the node, llama-server and tray logs of every online node. Task prompts are left out unless you tick
that box. Nothing is sent anywhere.

**Uptime monitors.** `GET /healthz` needs no login and returns `{"ok":true,"nodes_online":N,"tasks_active":M}`
(503 while the controller is shutting down). It shows counts only: no names, versions or addresses.

