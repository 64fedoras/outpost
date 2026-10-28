# Outpost architecture (v0.7.0)

## Processes and data
**Controller** (`outpost controller`, default :8443, HTTPS with a self-signed certificate). Data dir (`/var/lib/outpost/controller` as root on Linux): `admin.json` (PBKDF2 password hash), `secret.key` (encrypts the Claude and Hugging Face keys), `settings.json`, `sessions.json`, `nodes.json`, `tls.crt`/`tls.key`, `tasks/<id>/` (task.json and repo files), `models/<name>/` (controller-stored models), `runtime/<variant>/` (llama.cpp archives), `releases/<platform>` (uploaded Outpost binaries), `agent/`.

**Node** (`outpost node`). Data dir: `node.json` (controller URL, certificate fingerprint, node key), `control.json` (token for the local control server), `paused`, `node.lock`, `llama/` (installed llama.cpp builds), `models/`, `work/` (job scratch, swept at startup), `agent/` (versions of the binary).

## Connection and security
A node joins with a one-time code containing the controller URL, its certificate fingerprint and a secret. After joining it keeps a WebSocket to `/v1/node/ws`, pinned to that fingerprint, and sends a hello plus heartbeats (hardware, loaded model, paused flag, running jobs). The controller pushes jobs, settings, model files, runtime installs and updates over the same socket. Exec jobs run commands as the node's service user and are off unless enabled per node.

## Task lifecycle
Statuses: `planning`, `awaiting_answers`, `awaiting_approval`, `running`, `testing`, `fixing`, `queued` (waiting for the project slot), `paused_cap`, `done`, `failed`, `cancelled`, `interrupted` (set for in-flight tasks at controller restart; Resume continues from finished subtasks).
1. A message creates a **round**. The planner (Claude API or a local node) returns either questions or a plan: title, summary, language, setup and test commands, and subtasks with files, dependencies and instructions.
2. The user answers, replies with changes (re-plans) or approves.
3. The dispatch loop (`executeTask`) repeatedly takes ready subtasks and gives each to the first free node that has a loaded model. A node call is one inference at a time. The node writes whole files in a `<<<FILE path>>> … <<<END>>>` format.
4. After a subtask, another node reviews it (optional). Steering notes are added to every prompt that starts afterwards.
5. The controller runs the plan's test command on an exec node (files under 1 MiB are sent), then asks the model to fix failures for a limited number of rounds.
6. A result message lists the files; later messages start a new round on the same files.
Claude spend is metered per call and a cap pauses the task (`paused_cap`).

## Projects
`projects.go`, `projects_git.go`, `attachments.go`. `projects/<id>/project.json` (schema 1; git token and SSH key encrypted with `secret.key`) and `projects/<id>/repo/`, which is a git working tree when the project has a URL. `Task.ProjectID` points a task at its project and `Task.repoDir()` then returns the project's repo; `.git` is skipped by `listRepo`/`zipDir` and refused by `writeRepoFile` and zip extraction. `execTask` wraps `executeTask`: it takes the project's one-slot channel (status `queued` while waiting), and after a `done` round commits and optionally pushes (`exportRound`) and opens a GitHub PR. Old tasks have no `project_id` and need no migration; a task whose project is missing is unfiled on load. Later rows (project instructions, large codebase index) add fields to `Project` and files beside `repo/`.

## Model and runtime management
A model library entry has storage `controller` (downloaded once, copied to nodes) or `direct` (each node downloads from Hugging Face). Files are checked against Hugging Face SHA-256 values. The controller picks the llama.cpp build from GitHub releases per node (Vulkan, CPU, macOS) and pushes it. A node can run llama-server alone, or lend memory to a pool through ggml-rpc-server, bound to loopback behind an IP-allow gatekeeper.

## Updates
Binaries are uploaded in the web UI per platform; the controller reads each one's version from an embedded `OUTPOST_BUILD_INFO|<version>|` string, serves it to nodes, and keeps the previous build for rollback. On Windows a running exe cannot be replaced, so the node runs under a launcher that starts the newest installed version and hands over to it (0.6.1: a fresh process and a per-data-dir lock). Since 0.7.0 a Windows node installed from an admin shell is the `OutpostNode` service (`service_windows.go`): the service process is only a launcher that runs `outpost node` as a child inside a kill-on-close job object, restarts it on exit code 75 (update), and stops the service on 76 (Quit from the tray). The tray started with `--service` never starts the node itself.

## Hardening (0.7.0)
`controller_auth.go`: per-IP login lockout (in memory), session metadata in `sessions.json.meta`, API tokens (`api_tokens.json`, SHA-256 hashed; `auth` accepts cookie or bearer, `authSession` only the cookie). `controller_tls.go`: the controller serves `c.cert` through `GetCertificate`; a swap sends `node.repin` to connected nodes, waits for `node.repin_ack`, then writes `tls.crt/key` and switches; nodes that missed it are listed in `tls_repin.json` until they reconnect (`outpost repin` on the node). `backup.go`: tar.gz in 1 MiB AES-GCM chunks (final chunk flagged, so truncation is detected), scheduled copies via `backupLoop`.

## Web UI
Vanilla JavaScript single-page app embedded in the binary, no build step: sidebar shell, task thread rendered by keyed items, CSS variables with dark mode, mobile drawer. Talks to a JSON API under `/api/` (state, tasks, messages, answers, approve, models, nodes, tokens, releases, settings) with a session cookie.

## Platforms
Linux amd64 and arm64, macOS amd64 and arm64, Windows amd64, all built with CGO disabled. Linux service via systemd; tray icon via D-Bus StatusNotifierItem (Linux) and Win32 (Windows); none yet for macOS.
