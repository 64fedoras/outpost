# Outpost roadmap (all rows built, 0.7.0 and 0.8.0)

Status: **Rows 1 to 4 shipped in 0.7.0; rows 5 to 23 shipped together in 0.8.0.** The code is at v0.8.0. Plan written 2026-10-26 and edited 2026-10-27; status updated 2026-10-28.
The release labels below (0.8, 0.9, 1.0, 1.1) are the original grouping. At the owner's request they were folded into one release, **0.8.0**, so nothing here ships as 0.9, 1.0 or 1.1. The next release after 0.8.0 is 0.9.
Release numbers go up by one digit only (0.9 is followed by 1.0, then 1.1).

All rows were approved by the owner on 2026-10-28 and built. **Nothing in 0.8.0 has been tested on the real cluster, real Windows or a real Mac**; see the 0.8.0 build notes below.

| # | Feature | Release | Size | Decision |
| --- | --- | --- | --- | --- |
| 1 | Windows service and code signing | 0.7 | L | Built in 0.7.0 |
| 2 | Login hardening and API tokens | 0.7 | S | Built in 0.7.0 |
| 3 | Your own TLS certificate | 0.7 | S | Built in 0.7.0 |
| 4 | Backup and restore | 0.7 | M | Built in 0.7.0 |
| 5 | Node benchmarks and smart routing | 0.8 | M | Built in 0.8.0 |
| 6 | Best-of-N on hard subtasks | 0.8 | M | Built in 0.8.0 |
| 7 | Auto-resume after a restart | 0.8 | S | Built in 0.8.0 |
| 8 | File browser with per-round diffs | 0.8 | M | Built in 0.8.0 |
| 9 | Live output while subtasks write | 0.8 | M | Built in 0.8.0 |
| 10 | Notifications (browser, ntfy, webhook) | 0.8 | S | Built in 0.8.0 |
| 11 | Projects, attachments and git export | 0.8 | L | Built in 0.8.0 |
| 12 | Model picker sized to each node | 0.8 | M | Built in 0.8.0 |
| 13 | Node-to-node model transfers, hash-checked | 0.8 | L | Built in 0.8.0 |
| 14 | macOS service and tray | 0.8 | M | Built in 0.8.0 |
| 15 | Toolchains on nodes | 0.8 | M | Built in 0.8.0 |
| 16 | Build outputs and zips | 0.8 | M | Built in 0.8.0 |
| 17 | Working on a large codebase (index, .outpostignore) | 0.8 | L | Built in 0.8.0 |
| 18 | Agent mode for fixing and investigating | 0.8 | L | Built in 0.8.0 |
| 19 | Images and screenshots | 0.8 | M | Built in 0.8.0 |
| 20 | UI mockups and visual checks | 0.8 | M | Built in 0.8.0 |
| 21 | Project instructions (OUTPOST.md) | 0.8 | S | Built in 0.8.0 |
| 22 | Web lookups | 0.8 | S | Built in 0.8.0 |
| 23 | Publish a build to the Updates tab | 0.8 | S | Built in 0.8.0 |

Sizes: S is about a session of work, M two or three, L more than three. Each release ends with the build, test and zip routine described in `CLAUDE.md`.

What already exists and is built on, not rebuilt: a password login with 7-day sessions, self-signed TLS that nodes pin by fingerprint, a Resume button for interrupted tasks, a single-file viewer, and controller-stored models that nodes download over the LAN.

---

## 0.7: Hardening

Makes Windows nodes update reliably without fighting Defender, tightens the login, and lets the controller VM be rebuilt without losing anything.

### 1. Windows service and code signing

Windows nodes currently run as a per-user process started from the HKCU Run key, so they only run while the user is signed in. Updates also depend on the process handover fixed in 0.6.1.

- **Service:** `outpost install node` on Windows registers an `Outpost Node` service through the Windows service manager (`golang.org/x/sys/windows/svc`; the module is vendored already, the `svc` package may need adding with `vendor_sync.sh`). It starts at boot, restarts on failure, and an update becomes a service stop, a file swap and a start, with no second process to coordinate. Data moves to `C:\ProgramData\outpost\node`, which is already the install folder.
- **Tray:** the tray stays a per-user app and talks to the service over the existing 127.0.0.1 control server. Pause, Quit (now Stop service) and Change controller work as they do today.
- **Signing:** sign `outpost.exe` at build time with `osslsigncode`, which runs on the Linux build machine. Two options:
  - **Azure Artifact Signing**, about $10 a month (confirm in Azure's pricing calculator). Individual developers are eligible only in the US and Canada. Microsoft says signing does not give instant SmartScreen trust, but reputation builds across releases signed by the same identity.
  - **A self-signed certificate** imported into Trusted Publishers on the owner's own machines. Free, and stops "unknown publisher" prompts, but does not help with Defender's machine-learning detections.
- **Fallback, whatever the signing choice:** the installer adds the Defender exclusion for `C:\ProgramData\outpost` itself, with a printed explanation, instead of telling the user to run the command.

Risk: llama.cpp's Vulkan backend under a Windows service (session 0) must be tested on the 16 GB GPU machine (windows-desktop) before this ships. If the GPU is not usable there, the service starts llama-server in the user's desktop session through the tray instead.

Sources: [Code signing options for Windows apps](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options), [Artifact Signing pricing](https://azure.microsoft.com/pricing/details/trusted-signing/).

### 2. Login hardening and API tokens

The web UI already has a password, HttpOnly secure cookies and a 0.5-second delay on a wrong password (`handleLogin` in `controller.go`).

- **Lockout:** after 10 failed logins from one IP in 15 minutes, refuse that IP for 15 minutes, and show the count on the Settings page.
- **API tokens:** named tokens created in Settings, sent as `Authorization: Bearer …`, so scripts and the ntfy or webhook features can call the API without a browser session. Stored hashed, each with a last-used time and a Revoke button. `c.auth` accepts either a session cookie or a token.
- **Sessions list:** active sessions shown in Settings, with Sign out everywhere.

### 3. Your own TLS certificate

The controller generates a self-signed certificate, which browsers warn about. Settings gets an upload for a certificate and key, for example from a home-lab CA or a DNS-01 Let's Encrypt certificate. Nodes keep pinning by fingerprint, so after the swap the controller pushes the new fingerprint to connected nodes before it starts using the new certificate. A node that was offline during the change re-pins after the user confirms it in the Nodes tab.

### 4. Backup and restore

- **Backup:** Settings → Download backup produces one `.tar.gz` of `admin.json`, `settings.json`, `nodes.json`, `sessions.json`, `secret.key`, the TLS files and `tasks/`. Models, runtimes and releases are left out by default because they are large and can be downloaded again; a checkbox includes them.
- **Encryption:** `secret.key` protects the Claude API key, so the archive is encrypted with a passphrase typed at download time.
- **Restore:** `outpost install controller --restore backup.tar.gz` on a fresh VM. Nodes reconnect on their own because the node keys and TLS certificate come back unchanged.
- **Scheduled backups:** optional nightly copies to a folder, for example an NFS mount, keeping the last 7.

---

### 0.7.0 build notes (what shipped and what did not)

- **Row 1:** shipped the `OutpostNode` service (launcher plus job object), the admin-shell installer with automatic Defender exclusion, per-user fallback, `uninstall node`, and an optional signing step (`winres/sign.sh`, `winres/selfsign.sh`). The default is **unsigned**: no certificate is configured, so the build is unchanged until you set `OUTPOST_SIGN_PFX` (osslsigncode) or `OUTPOST_SIGN_CMD`. The Azure Artifact Signing versus self-signed decision is still open. Untested: the service on real Windows, Vulkan under session 0, signing, Defender.
- **Row 2:** shipped as written (lockout count on Settings, sessions list, Sign out everywhere, hashed bearer tokens).
- **Row 3:** shipped. Offline nodes are re-pinned with `outpost repin --fingerprint …` on the node, not with a button in the Nodes tab (a node that cannot connect cannot be re-pinned from the controller). Nodes on 0.6.1 ignore the push, so update them first.
- **Row 4:** shipped, including nightly copies. Restore is `install controller --restore FILE`.

### 0.8.0 build notes (rows 5 to 23: what shipped, what did not)

All of this is **untested on the real cluster, on real Windows and on a real Mac**. It was tested with Go unit tests and the e2e rig (stub Claude and OpenRouter servers, fake llama-server, fake Chromium). Where a row is not listed, it shipped as written.

- **Row 5:** shipped. Benchmark speeds and routing only seen against stubs.
- **Row 6:** shipped. The setting defaults to **Hard subtasks**, as planned. Extra copies only run on nodes nothing is waiting for.
- **Row 13:** shipped hash-checked node-to-node transfers (port 8444, up to 3 peers, 64 MiB chunks). **Left out:** the Release storage button, freeing the controller's copy after an upload, and the background hash re-check before first load. Controller storage is kept as an optional seed (this answers the open question).
- **Row 14:** shipped the per-user **LaunchAgent only**. The menu bar tray is not built: it needs cgo and a macOS runner.
- **Row 15:** shipped the tool inventory, routing by tool, the Go installer and PowerShell on Windows exec nodes. **Chromium is detected, not installed.**
- **Row 16:** shipped. **Failed builds do not trigger fix rounds**, and files over 1 MiB need the controller to reach the node on port 8444.
- **Row 17:** shipped. The file summary in the index is the **first comment line**, not written by a node.
- **Row 18:** shipped. Agent fix rounds cost more per round than the old single request (the conversation is re-sent every step).
- **Row 19:** shipped. The **mmproj file names and sizes in the catalog are assumptions** and need checking on first download.
- **Row 22:** shipped. The **Claude web search tool name and its price are unverified** against the live API.
- **Rows 7 to 12, 20, 21, 23:** shipped as written. Row 21 is a single Settings box (plus an `OUTPOST.md` in the task's files) until per-project instructions are added.

## 0.8: Smarter cluster

Sends each subtask to the node best suited to it and puts idle nodes to work on quality. Today the dispatcher (`executeTask` in `tasks.go`, around the `workerNodes()` loop) gives each ready subtask to the first free node in ID order, whatever its model or speed.

### 5. Node benchmarks and smart routing

- **Measure:** when a model finishes loading, the node runs a fixed benchmark (a 1,000-token prompt and 256 generated tokens) and reports prompt and generation speed. Real subtasks already record tokens per second (`SubtaskState.TokPerSec`), and a rolling average of those replaces the benchmark figure over time. Figures appear on the Nodes tab.
- **Model strength:** each model gets a tier, Small, Medium or Large, guessed from its parameter count and editable on the Models tab.
- **Difficulty:** the planner tags every subtask `easy`, `normal` or `hard` in the plan JSON, and the plan card shows the tag so it can be changed before approving.
- **Routing:** hard subtasks go to the strongest free model and wait up to 2 minutes for one before taking any free node. Easy subtasks go to the fastest free node, which keeps small nodes busy instead of idle. Normal subtasks take the best score from tier and speed.
- **Reviews:** a review goes to a node at least as strong as the writer when one is free, instead of any other node.

### 6. Best-of-N on hard subtasks

When a hard subtask starts and 2 or more extra nodes are idle, it also runs on up to 2 of them. Candidates that fail a quick syntax check (`python -m py_compile`, `go vet`, `node --check` and so on, on an exec node) are dropped. A judge node then compares the rest against the instructions and picks one, and the log says which node won and why.

It never delays other work: extra copies only start on nodes no ready subtask is waiting for, and they are cancelled if a subtask needs the node. A setting picks Off, Hard subtasks (default) or All subtasks. It runs only on local nodes, so it adds no Claude cost.

### 7. Auto-resume after a restart

Resume already continues an interrupted task from the subtasks that finished (`handleResumeTask`). Today the user presses it after every controller restart or update. In 0.8, tasks interrupted mid-run resume on their own once at least one node with a loaded model reconnects, waiting up to 10 minutes.

Tasks that were planning with Claude are the exception: they stay interrupted, so a restart never spends money without the user. A setting turns auto-resume off. A subtask whose node drops mid-write is already requeued, and that stays as it is.

---

## 0.9: Workflow

Makes a task something you can leave running and come back to: see the files and what changed, watch work as it happens, get pinged when needed, and keep a project across tasks.

### 8. File browser with per-round diffs

- **Files panel:** a file tree beside the thread replaces today's single-file viewer (`/api/tasks/{id}/file`), with syntax highlighting, line numbers and a download button per file.
- **Changes:** before each round writes anything, the controller keeps a copy of the files that round touches in `tasks/<id>/rounds/<n>/`. Each result card gets a Changes button that opens a side-by-side or unified diff of that round, computed in the browser with a small built-in diff (no new dependency).
- **Undo a round:** a button restores the files from that round's copy, recorded in the thread as an event.

### 9. Live output while subtasks write

The node asks llama-server for a streamed response (it already streams internally, see `chat` in `node_llama.go`) and forwards text over the existing WebSocket roughly every 250 ms. The controller keeps the last 64 KB per running subtask in memory only, and the browser receives it through a new server-sent events stream (`/api/tasks/{id}/events`), which also replaces polling for status changes. A running subtask in the thread expands to show its text appearing, with live tokens per second. Planning with Claude streams the same way.

### 10. Notifications

- **Events:** the planner asked a question, a plan is ready to approve, a task finished or failed, the spending cap paused a task, or a node has been offline for 5 minutes. Each can be switched on or off.
- **Channels:** browser notifications while a tab is open; ntfy (set a topic URL, works with a self-hosted ntfy server); a webhook (JSON POST to any URL, for Home Assistant, Discord, or anything else that accepts one).
- **Links:** every notification links straight to the task.

### 11. Projects, attachments and git export

- **Projects:** a project keeps its files plus defaults (planner, review on or off, test command). Every task in it starts from the project's current files, the way a follow-up round does today. The sidebar groups tasks under their project. One task runs per project at a time and later ones queue, which avoids two tasks writing the same files. Tasks with no project work as they do now. Needs a migration for existing tasks: they become one-task projects or stay unfiled.
- **Attachments:** the composer takes files or a `.zip` (up to 50 MB). A zip becomes the task's starting files; loose text files go to the planner as context. Images are left out here (see 19).
- **Git:** a project can start from a git URL (cloned on the controller) and push back after each round: one commit per round, with the request as the message, to a branch the user chooses. Credentials (an HTTPS token or deploy key) are encrypted with `secret.key`. It uses the `git` command on the controller VM, which the installer checks for. Opening a GitHub pull request is optional and needs a token with repo access.

---

## 1.0: Models and nodes

Makes choosing and placing models less guesswork, moves models between nodes safely, and brings macOS up to the other platforms.

### 12. Model picker sized to each node

- **Fit badges:** the Hugging Face file list on the Models tab (`handleHFFiles`) already shows the GGUF files in a repo. Each file gains a badge per node: fits in VRAM, fits split across VRAM and RAM (slower), or does not fit. The estimate covers the weights plus the KV cache at the chosen context size.
- **Suggestions:** a short built-in list of good coding models, refreshed each release, ranked by what fits the largest node.
- **Load where it fits:** after download, one button loads the model on every node it fits fully in VRAM.

### 13. Node-to-node model transfers, checked against controller hashes

Models move between nodes over the LAN instead of from the controller. The controller keeps only each file's SHA-256 hash and the list of which nodes hold it. Nodes check every transfer against that hash, so a tampered or corrupted file is never loaded. (Today a model is either stored on the controller and copied from there, `storage: "controller"`, or downloaded from Hugging Face by every node, `storage: "direct"`; see `controller_models.go`.)

**Where the hash comes from.** The controller records the hash before any node downloads anything, so no node can vouch for its own copy:

- **Hugging Face models:** the controller reads the SHA-256 from the Hugging Face API (the LFS object ID, already read at `controller_models.go` ~line 811) over verified HTTPS when the model is added.
- **Uploaded GGUF files:** the controller hashes the file as it arrives, sends it to one node, then deletes its own copy.
- **Existing controller-stored models:** these keep their hashes and become seeds. A Release storage button sends a model to one node and frees the controller's disk.

**How a transfer works:**

1. A node needs a model, so the controller picks up to 3 peers that hold it and are online, preferring idle ones. It sends the node the peer addresses, each peer's TLS certificate fingerprint, a one-time transfer token, and the expected SHA-256.
2. All of this goes over the node's existing WebSocket to the controller, which is already pinned to the controller's certificate. Someone on the network cannot swap the hash or the peer list.
3. Each peer serves files on a new port (default 8444) with its own self-signed certificate. The downloading node pins that peer's fingerprint, so it cannot be pointed at an impostor, and the peer refuses requests without a valid token.
4. The file is fetched in 64 MiB chunks, split across the peers, and resumes after a dropped connection. The first node to verify a file also publishes a list of chunk hashes, so a bad chunk is caught and fetched again from another peer instead of failing the whole file at the end.
5. Once the whole file is downloaded, the node checks the full SHA-256 against the controller's value. Only a match is moved into the model folder and reported as held. A mismatch is deleted, logged with the peer's name, and that peer is skipped for 1 hour.
6. If no peer has the model, the first node downloads it from Hugging Face, checks the same hash, and becomes the seed.

Nodes also re-check a model's hash in the background before first load after an update, which catches disk corruption. The peer port goes into the installer's firewall rules on Linux and Windows.

Decided: controller storage stays as an optional seed (see the 0.8.0 build notes for what was left out).

### 14. macOS service and tray

macOS nodes already run and use the llama.cpp macOS builds, but only from a terminal.

- **Service:** `outpost install node` writes a per-user LaunchAgent. It has to be per-user, not a system daemon, so llama.cpp can use the GPU through Metal. Pure Go, so it builds on the current machine.
- **Tray:** the menu bar icon needs Apple's frameworks through cgo, which cannot be cross-compiled from a Linux build machine. It needs either a Mac to build on or a GitHub Actions macOS runner. Without one, ship the service in 1.0 and the menu bar icon later.

---

## 1.1: Building Outpost in Outpost

Closes the gaps so Outpost can be iterated on from its own task page: build all five binaries, look at the UI, and publish the result to the Updates tab. Depends on Projects (11) and on the chunked transfer code from model transfers (13).

### Gap check: what works today for working on Outpost itself

| Cowork ability | Outpost today (v0.6.1) | What is missing |
| --- | --- | --- |
| Compile Go, run commands | Partly. The plan's setup and test commands run with `sh -c` on an exec-enabled Linux node, using whatever is installed, with a 10-minute limit (`testTimeout`, `runExecJob`). | The planner does not know which tools a node has. Windows nodes have no `sh`. Commands run only as tests, never as a build step. |
| Return build outputs | No. Files over 1 MiB cannot travel to or from an exec node (`inlineMax` in `node.go`, `maxRepoFileSize` in `tasks.go`). | Outpost's own binaries are several MB each, so a compiled build cannot come back. |
| Zip files | Partly. The task's files download as one `.zip`. | No zip of build outputs or of chosen files. |
| Read screenshots and images | No. Tasks take text only, and the local models read text only. | Image input for the planner and for vision-capable local models. |
| UI mockups | Partly. Models can write HTML and CSS. | Nothing renders the page or looks at the result. |
| Work on a large codebase | Not in practice. About 11,700 lines of Go plus 113 KB of web files, roughly 150k tokens. | More than a local model's context. The project snapshot is cut to fit, and `vendor/` (2.3 MB) would compete for the same room. |
| Read, run, look, fix | Partly. A fixed plan, write, test, fix pipeline. | Workers cannot open other files, search or run commands themselves. |
| Ask questions mid-work | Partly. Only the planner asks, before the plan. | A worker or fixer that gets stuck cannot ask the user. |
| Project instructions (like CLAUDE.md) | No. | Standing rules for a project. |
| Web lookups | No. | Reading docs for a library or API. |
| Ship what it built | No. | A way to send a finished build to the Updates tab. |

### 15. Toolchains on nodes

- **Inventory:** each node reports the tools it has in its hello message and refreshes the list hourly: `go`, `python3`, `node`, `gcc`, `git`, `zip`, and a headless Chromium. The Nodes tab shows them.
- **Planning and routing:** the planner prompt lists what is available. Each command is sent to a node that has the tool it names, so `go build` goes to a node with Go.
- **Go installer:** like the llama.cpp runtime install today. The controller downloads the official tarball once, checks its SHA-256, and pushes it to the exec nodes the user picks. Outpost needs Go 1.24 or newer (see `go.mod`).
- **Windows:** exec nodes run commands with PowerShell instead of `sh`, and the planner is told which shell each node uses.

### 16. Build outputs and zips

- **Large files:** files of any size, up to 2 GB per job, move between the controller and exec nodes in hash-checked chunks, using the same code as model transfers.
- **Build step:** the plan gains a `build_command` (for example `./build.sh`) and `artifacts` patterns (for example `dist/*`). Builds run after the tests pass.
- **Results:** artifacts appear on the result card and download one by one, as `artifacts.zip`, or with the source in the task zip. The file browser can also zip a set of files the user ticks.

### 17. Working on a large codebase

- **Index:** instead of pasting the whole project into the prompt, the controller keeps an index with each file's path and size, its functions and types (parsed with Go's own parser for `.go` files, simpler patterns for JS and CSS), and a one-line summary a node writes.
- **Planner:** gets the index plus the files it asks for by name, in a short first exchange. That keeps a Claude-planned round on Outpost to a bounded cost.
- **Workers:** get their own files plus the declarations from files those depend on, not whole other files.
- **`.outpostignore`:** like `.gitignore`. It keeps `vendor/` and `dist/` out of prompts while they stay in the project for builds and tests.

### 18. Agent mode for fixing and investigating

Workers and fixers get tools through llama-server's tool-calling support (started with `--jinja`) and the Claude API's tool use:

- `read_file`, `list_files`, `search`
- `write_file`
- `run`, which executes on an exec node
- `ask_user`, which posts a question in the thread and pauses only that subtask

They can read a failing test, open the file it points at, try a fix and re-run, which is the loop Cowork uses. Each run is limited to 30 tool calls and 20 minutes. Agent mode replaces the single-shot fix rounds, and the planner can schedule an `investigate` subtask when a request needs digging before code is written.

### 19. Images and screenshots

- **Attachments:** the composer takes images such as PNG, JPEG and WebP.
- **Claude planner:** images go to Claude as image input.
- **Local planner:** images go to a node running a vision model. llama-server supports these with a separate projector file (`--mmproj`), which the Models tab downloads alongside the model. The model is then marked Vision.
- **Without a vision model:** if no vision model is loaded and the planner is local, the composer says so instead of dropping the image.

### 20. UI mockups and visual checks

- **Screenshots:** an exec node with headless Chromium renders HTML pages, or starts the app and opens it, at desktop (1280 px) and phone (390 px) widths. The screenshots appear in the thread.
- **Visual review:** after UI subtasks, a vision model or Claude compares the screenshots with the request and can trigger a fix, so it looks at what it built.
- **Mockup tasks:** a mockup task produces 2 or 3 variants side by side to pick from before any real code is written.

### 21. Project instructions

Each project has an `OUTPOST.md` at its root, editable in project settings, that goes into every planner, worker and reviewer prompt. For Outpost it would hold rules such as "vendor all dependencies, no cgo on Windows, build with build.sh, keep the web UI vanilla JS" (the same rules as `CLAUDE.md` in this repo).

### 22. Web lookups

- **Pasted links:** the controller fetches URLs pasted in messages as readable text and gives them to the planner.
- **Claude planner:** can optionally use the Claude API's web search tool, which is charged per search and counted against the task's spending cap. Off by default.

### 23. Publish a build

When a task's artifacts include `outpost-<platform>` binaries, the result card offers Publish to Updates, which uploads them as a release (`handleUploadRelease`). Updating the controller or nodes is still the user's click on the Updates tab, and rollback works as it does now. A task never updates the controller it is running on by itself.

**Expectations:** local models alone will struggle with Outpost-sized changes. The realistic setup is Claude as planner and judge, the largest model on the big node (node02) for hard subtasks, and the GPUs for the rest. Agent mode and the index are what make that workable.

---

## Testing, risks and open questions

Every feature gets Go unit tests and an end-to-end run with the mock Claude and fake llama-server (`testing/`), plus Playwright screenshots for UI work. The Windows and macOS parts are where testing falls short.

| Area | How it is tested without the real hardware | Gap the owner covers |
| --- | --- | --- |
| Windows service, signing, Defender | Cross-compile, `go vet`, signature check with `osslsigncode verify` | Install and update on windows-desktop, including GPU use under the service |
| Routing, best-of-N, auto-resume | Fake nodes with set speeds and tiers; kill and restart the controller mid-task | Real speed differences across the four nodes |
| Live output, notifications | Fake llama-server that streams; local ntfy and webhook receivers | Phone pushes from the real ntfy topic |
| Git export | Local bare repo as the remote | Push to the real remote |
| macOS | Build only | A Mac to run it on |
| Peer model transfers | Three local nodes, including a peer serving a tampered file, one presenting the wrong certificate, and a connection dropped mid-chunk | Real transfer speeds between the machines |
| Outpost in Outpost | A task on a copy of the Outpost repo that builds all five binaries and screenshots the UI, with mock models | A real round with Claude and the big node, to judge quality |

**Main risks**

- **GPU under a Windows service:** if llama.cpp cannot use the GPU from session 0, the fallback (llama-server started by the tray) adds complexity back.
- **Best-of-N cost:** it uses spare capacity but makes hard subtasks wait for the judge. If the gain is not visible in practice, it stays off by default.
- **Projects:** touches the task model most, so it needs a migration for existing tasks.

**Open questions for the owner**

- [ ] Is the release order right, or should 0.9 (workflow) come before 0.8 (smarter cluster)?
- [ ] Should 1.1 move ahead of 0.8 to 1.0 so iterating on Outpost in Outpost can start sooner? It needs Projects (11) and model transfers (13) first.
- [ ] Code signing: Azure Artifact Signing, a self-signed certificate on the owner's own machines, or skip signing and rely on the Defender exclusion?
- [ ] Is there a Mac for the macOS build and test, or should macOS stay a service without the menu bar icon?
- [ ] Which git host would export push to: GitHub, a self-hosted Gitea or Forgejo, or something else?
- [ ] Is ntfy, Home Assistant or Discord already in use, so notifications target what is used?
- [x] Model transfers: controller storage kept as an optional seed.
- [ ] For working on Outpost: which node gets Go and Chromium, node02 or the gaming desktop?
