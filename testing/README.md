# Testing

## Run everything
One entry point, `sh testing/run_all.sh`:

| Command | What it runs |
| --- | --- |
| `sh testing/run_all.sh` (or `fast`) | A `gofmt` check, `go vet`, all unit tests, `node --check` on every `web/*.js`, and a build for all five platforms. |
| `sh testing/run_all.sh race` | Unit tests with the race detector (needs gcc). |
| `sh testing/run_all.sh e2e` | Starts the local rig, runs the browser scripts below (`groups`, `steer`, `edit`, `rerun`, `taskmenu`, `projmenu`, `mobile`, `dropzone`, `projectnew`, `chatpage`), stops the rig. Needs Python Playwright; it stops with an error if that is missing instead of skipping. `E2E_FULL=1` adds `flow.py` (about 4 minutes) and `soak.py`. |
| `sh testing/run_all.sh all` | `fast`, then `race`, then `e2e`. |

CI runs `fast` and `race` on every pull request. `e2e` is run by hand (the nightly soak workflow runs `soak.py`).
Tests live beside the code they cover and are named for it; a test checks that no new file is named after a pull request number.

## Unit tests
The Linux sandbox tests (`internal/sandbox/linux_test.go`) run real sandboxed commands and skip themselves on kernels without Landlock.
    go vet ./... && go test ./...
    CGO_ENABLED=1 go test -race ./...     # also run on every pull request

Every pull request also runs `govulncheck` (informational: it reports, it does not block).

## End-to-end rig (no model, no API key)
`testing/e2e/run.sh up` builds with `-tags e2e`, starts a real controller and a real node on
127.0.0.1, a stub Claude API (`mock_claude.py`, key `sk-test-key`) and a fake llama-server
(`fake_llama_server.py`, shipped inside the stub llama.cpp archive in `fixtures/ctl-seed`).
It enables exec on the node and loads the 1 MiB `tiny-test` model. State is in
`testing/e2e/run/` (git-ignored); `run.sh down` stops everything, `run.sh pw` prints the
admin password (chosen through the first-run setup endpoint; it is in `run/pw.txt`). UI: https://127.0.0.1:18460 (self-signed).

Then, with `OUTPOST_E2E_DIR=$PWD/testing/e2e/run`:

| Script | What it does |
| --- | --- |
| `flow.py dark\|light` | Full chat flow: question cards, plan approval, steering note, result, follow-up round. About 3 to 4 minutes, because the fake model sleeps 4 s per call. Screenshots go to `run/chat/`. |
| `steer.py` | Sends a note while the planner is working (a request containing "slow plan" makes the stub take 20 s): the planner finishes its answer, then revises it with the note; a queued note can be removed or sent now (interrupts the call). |
| `edit.py` | Editing messages: before approval, then after a finished round (files undone, new round). Set `OUTPOST_CHROMIUM` to a Chromium binary if Playwright's own is missing. |
| `rerun.py` | Edit the plan (before approval, via the pencil on the plan card) and re-run an approved plan. |
| `taskmenu.py dark\|light` | Sidebar task menu: right-click, the ⋯ button, Shift+F10, F2 rename, pin, copy link and prompt, duplicate, delete, and the phone drawer. Screenshots go to `run/menu/`. |
| `mobile.py` | The same pages at phone width. |
| `usability1.py` | Usability batch 1: Adjust… on the plan card, Ask/Enter on the project composer, name-only New project dialog, Add node OS tabs; shots in `run/shots/usability1/`. |
| `usability2.py` | First-run checklist on New task (Add a node / Pick a model buttons, card gone once a node has a model); shots in `run/shots/usability2/`. |
| `usability3.py` | Update everything: one primary button, per-machine buttons behind "Choose individually…", one Check button, folded uploads, badge matches the page; shots in `run/shots/usability3/`. |
| `usability4.py` | Monthly limit asked once on the first cloud key (Set limit / No limit); Scheduled shows the browser clock when it differs; shots in `run/shots/usability4/`. |
| `usability5.py` | Settings autosave: planner defaults, running tasks and free-disk cards have no Save button and save after a change (including reordering Auto); shots in `run/shots/usability5/`. |
| `assume.py` | Plan first with assumptions: Assumed row on the plan card, a changed chip turns the button into Re-plan, Other… takes a typed value, a clear request has no row; shots in `run/shots/assume/`. |
| `projectnew.py` | Projects page: "New task in this project" in a card's menu opens the new-task page with that project picked. |
| `chatthink.py` | Chat thinking behind a fold (closed by default, stays open when opened, kept after reload), the green working bar, and an answer cut at the length limit carried on automatically. |
| `chatpage.py` | Chat page layout: model cards on a new chat, the docked composer, the chats panel and the phone sheet. |
| `chatversions.py` | Chat edit and resend, regenerate, version arrows, branch, per-chat instructions and the Chats search box (desktop light and dark, phone). |
| `projectreads.py` | Project conversation reads files: chips for list, search and read calls, `path:line` links opening the read-only file viewer (light and dark). |
| `preview.py` | Live preview: HTML and Mermaid answers become cards; the sandboxed panel runs scripts with network and cookies blocked, Code tab, versions, Save to project, dark and phone. |
| `memory.py` | Memory: add a fact in Settings, take a suggestion under a chat answer with Edit first, edit and list (light, dark, phone). |
| `docs.py` | Documents in chat: a PDF and an XLSX through 📎 (composer chips, the model gets the text, message chips with pages, sheets and words, the Text link) and a scanned PDF's warning (light, dark, phone). |
| `runcode.py` | Run code in chat: turn it on for a new chat, the model's first Python program fails and the fixed one runs on the node for real; run cards (code, output, SVG chart, CSV download), toggle remembered (light, dark, phone). |
| `research.py` | Research mode: pick Research, lanes (waiting, working, done) against a DuckDuckGo stand-in in `mock_claude.py`, report with numbered sources, lane record and Open report panel, Stop marks lanes stopped (light, dark, phone). |
| `cad.py` | New 3D model page: a scripted model writes OpenSCAD (a typo first, fixed in round 2), the node renders it with the real OpenSCAD (needs `openscad` on PATH), the viewer draws it, a parameter re-renders and "Ask for changes" edits it. |
| `chat3d.py` | Chat to 3D: a clear 3D request opens the 3D card on Send (mesh note, look-up boxes), Answer in chat instead (and the chat stays in chat), a 3D-printing question answered in chat with Make this in 3D, C++ STL text never routed, and Make it with the measurement look-up shown on the 3D page (needs `openscad`; without it, shoots the Install OpenSCAD card). |
| `nodetools.py` | Nodes page notices "No node has Chromium/OpenSCAD": recommended node, Install posts the right request, Not now hides it. State is patched in the browser; nothing is downloaded. |
| `chatimages.py` | Chat screenshots: `novision` checks the warning and refusal with the text-only rig model; `vision` (after adding `mmproj-F16.gguf` to `run/ctl/models/tiny-test/model.json` `hf_files` and restarting the controller) sends a pasted image and checks the thumbnail and that the fake server got it. |
| `groups.py` | Sidebar groups: the single grouped list (status icons, status order), new group, dragging a task and a chat onto a group, Move to group, rename, colour, move, delete, and the phone sheet. Runs first: it expects a controller with no groups yet. |
| `schedules.py` | Schedule dialog (every few hours, chosen days and refusing none, monthly on the 31st, the preview line) and the Scheduled page: sidebar count, Run now history, open row, Pause/Resume, Pause all banner, ⋯ menu (Edit request, Duplicate, Remove schedule, Escape), phone layout. |
| `settingssearch.py` | Settings search: "api token" finds Security › API tokens first, no-match line, folded help opens on a match, Esc restores the open section, "/" focuses the box, Connect a tool tab opens for "ollama", the API keys pointer link, phone width. |
| `tokenscope.py` | Settings, Security: API token access is one choice (Chat only by default), each choice stores the right scope, Full access is stored as an empty scope like older tokens and is labelled "full access" in the list; light, dark and phone shots. Removes its tokens after. |
| `shots.py` | Screenshots of every tab (needs a task id edited into the script). |
| `soak.py` | No browser. Runs `SOAK_TASKS` tasks (default 8) through the API and, at random (`SOAK_SEED`), kills and restarts the node, the controller or both mid-task; every task must end `done`. Presses Resume for Claude tasks after a controller restart. Runs nightly in `.github/workflows/soak.yml`. |

Agent mode: the stub Claude answers requests that carry `tools` with a scripted fix (list, read,
write, run the tests, summary), and `fake_llama_server.py` answers them with streamed tool calls
(search, ask_user, report). A prompt containing "dig first" makes the stub plan an `investigate`
subtask; answer its question with `POST /api/tasks/<id>/agent-answer`. `run.sh up` repacks the
stub llama.cpp archive from `fake_llama_server.py` each time.

The fake model writes a deliberate bug in `wordcount/core.py` so the review and test-fix loop
has something to fix. `mock_claude.py` returns questions when the prompt contains "ask me
first", a one-subtask plan for follow-ups, and a fixed 3-subtask plan otherwise.

Playwright and Chromium are expected to be installed (`pip install playwright && playwright install chromium`).

## Screenshots (visual.go)
`TestRenderShotsWithBrowser` and `TestVisualCheckFixesWhatItSees` run a fake node over a real
WebSocket that executes the Chromium job locally. They use a headless Chromium found under
`/opt/pw-browsers` or `OUTPOST_TEST_CHROME=/path/to/chrome-headless-shell`, and skip without one.

## Lab checks (the real cluster)
`testing/lab_assume.py` (run by hand, not in the Gitea workflow) compares planners on eight vague requests ("back up my
photos"): once with the local planner (`auto`) and once with Claude if a key is saved. It never approves a plan, deletes its
test tasks, and writes `lab-assume-report.html` (`OUTPOST_LAB_OUT`): planned at once or asked, the assumptions, time to a plan.
Same `OUTPOST_LAB_URL`/`OUTPOST_LAB_TOKEN` as below; Claude costs a few cents (cap $0.50 per task).

`testing/lab_smoke.py` talks to a real controller over its API: version and nodes, **Check cluster** on every
online node (a failed step on an online node fails the run), `GET /v1/models` and one tiny chat message.
It needs only Python 3 and two environment variables, so it also runs by hand:
`OUTPOST_LAB_URL=https://192.0.2.10:8443 OUTPOST_LAB_TOKEN=<API token> python3 testing/lab_smoke.py`.
Optional: `OUTPOST_LAB_FINGERPRINT` (pin the self-signed certificate; the run prints the value to use),
`OUTPOST_LAB_REQUIRE` (node names that must be online) and `OUTPOST_LAB_SKIP_CHAT=1`.

`.gitea/workflows/lab.yml` runs it on a LAN runner: a Gitea instance keeps a **pull mirror** of this repository
and an `act_runner`/`gitea-runner` with the label `lab-runner` takes the job. It starts by hand or nightly, never on
a push or pull request, so the cluster is not touched by every mirror sync. GitHub never runs it (it only reads
`.github/workflows/`). Gitea lists the mirror's `.github/workflows/` files too, but they need `ubuntu-24.04` runners,
which the lab runner does not have, so they only queue (cancel them or disable those workflows in Gitea; never give
the lab runner that label, or it would try the release build). No cluster address or token is in the repository: they are Gitea secrets
(`OUTPOST_LAB_URL`, `OUTPOST_LAB_TOKEN`, optionally `OUTPOST_LAB_FINGERPRINT`). Host jobs run as the runner's
user without isolation, so only point the runner at a mirror you control.
**To set this up on your own network from scratch** (mirror, runner machine, secrets, firewall troubleshooting, any OS mix), follow [`testing/LAB.md`](LAB.md). The runner is created by `scripts/lab-runner-setup.sh`, which is **untested on a real machine** (syntax-checked only).
**Stage `inspect`** (`OUTPOST_LAB_STAGES=smoke,inspect`, also the workflow default) adds read-only checks from
`testing/lab_features.py`: every online node runs the controller's version; Pick best (`load-options`) returns exactly
one best pick that fits, and a GPU node's pick is not CPU-only; each node's model-sharing port is reachable from the runner
(the Windows firewall rule; `OUTPOST_LAB_SKIP_PEER=name,name` skips nodes the runner cannot route to); Mac node logs
have no Local Network block; Windows node logs show no service errors; the update check answers. Nothing is
installed, loaded or restarted. Warnings say what it cannot see: tray icons, Windows service survival over a reboot,
Defender, and Apple Silicon/Metal when no arm64 Mac is online. Stages for update/rollback and memory pooling
(which change the cluster and will be manual-only) are partly built:
**Stage `update`** (`testing/lab_update.py`) **restarts one node three times**. In the workflow, start it with Run workflow,
stages `smoke,inspect,update` and `update_node` set to a spare node; by hand set `OUTPOST_LAB_STAGES=update` and
`OUTPOST_LAB_UPDATE_NODE=<name>`. It is refused on a scheduled run. The node must be idle and the controller must hold a
release for its platform (Updates tab, or fetched from the update repository). It updates the node if it is older,
rolls it back to its previous copy, then updates it again, and after each switch checks the node is back on the right
version within `OUTPOST_LAB_UPDATE_TIMEOUT` seconds (600), lists the same model files, kept its node settings and reloaded
a loaded model within 5 minutes. A rollback holds auto-install at the version left; the stage puts the hold back as it was
(`POST /api/github-updates/hold`), even after a failure. It never updates the controller.
**Stage `pool`** (`testing/lab_pool.py`) **changes two nodes**: set `OUTPOST_LAB_POOL_MAIN`, `OUTPOST_LAB_POOL_HELPER` and
`OUTPOST_LAB_POOL_MODEL` (a ready library model, ideally too big for main alone; the workflow has matching inputs).
Refused on a scheduled run; trusted LAN only (llama.cpp RPC has no authentication or encryption). It checks that a pooled
load is refused while pooling is off, switches pooling on, loads the model on main with the helper lending
(`OUTPOST_LAB_POOL_TIMEOUT`, default 1800 s), checks the helper's RPC port closes a connection from the runner at once
(the main-node-only allow-list), gets one chat answer, then switches pooling off and checks the pool dissolves and the
port closes. The pooling setting is put back and each node's own model is loaded again afterwards, even after a failure.
Not covered: a helper or main restart mid-pool, more than one helper, Windows or macOS helpers.
The inspect and update stages passed on the real cluster on 2026-11-22; the pool stage has run only against a stub controller; tray, phone, Windows install and real GPU loads are still by hand.

## Not covered by any script (test by hand)
- Screenshots on the real nodes: Chromium on each platform (snap Chromium on Ubuntu, Chrome on Windows
  and macOS), the 390 px phone width in new headless mode, and `preview_command` apps.
- Windows: tray icon, install, web-UI update (needs a real Windows machine; Defender behaviour).
- Real GPUs and Vulkan/ROCm builds of llama.cpp, memory pooling across machines (including the off-by-default Settings switch against a real ggml-rpc-server).
- macOS (builds only).
- Linux sandbox on the real nodes: Ubuntu 24.04 restricts unprivileged user namespaces with AppArmor (strict then falls back to Landlock TCP rules on kernel 6.7+, or refuses); check a node installed with `sudo` (root) and one run as an ordinary user, the node card badge, `go build`/`go test` with an Outpost-installed Go and a distro Go, git, Chromium screenshots (`visual.go` runs under the sandbox and may need `OUTPOST_SANDBOX_EXTRA_RO` or sandbox off on that node), and a test step with a download.
- Real Claude API cost figures.

- Windows experimental job-object limits (`OUTPOST_EXPERIMENTAL_WINDOWS_SANDBOX=1`): run `go test -run 'Job|Windows' ./cmd/outpost` on a real Windows machine and check a cancelled exec job leaves no child processes (Task Manager).
- Check cluster on the real nodes: a firewall that blocks the file-sharing port (the note's `ufw` / `New-NetFirewallRule` command), a node with a wrong clock, a Windows node's `echo` through PowerShell, and a node still on an older version (its Logs button says to update it).
- Node logs and Download diagnostics against Windows and macOS nodes (tray log path), and a nightly backup to an NFS folder that fills up or goes away mid-write (the Backup card and the *Backup failed* notification).
- One-node-first updates on the real cluster: a Windows node as the first node (Defender blocking the new exe should stop the rollout and leave the rest alone), a first node whose model takes minutes to load again, and GitHub auto-install with the rollout.
- The hung-model watchdog against a real llama-server that stalls (for example SIGSTOP the llama-server process during a call: after about 4 minutes it should be killed, restarted, and the subtask should move).
- Pause from the web UI on a node that also has a tray: the tray menu should show the web pause, and resuming from either side should clear it.
- The JSON-schema retry for local planners against the real llama.cpp builds Outpost installs (a build that rejects `response_format` should fall back silently; check `node.log`), and whether the schema slows a small model's plan noticeably.
- Hand-off zips opened in each assistant: Claude Code reads `CLAUDE.md`, Codex `AGENTS.md`, Gemini CLI `GEMINI.md`, Copilot `.github/copilot-instructions.md`, Cursor `.cursor/rules/*.mdc`. Only the file layout is unit-tested; none was tried in the real tools.
- `git apply` of a task's `.patch` on a project with CRLF files or binary files (binary changes are not in the patch).
- Model serving options on real llama.cpp builds: 8-bit and 4-bit context cache with flash attention on Vulkan, CPU and Metal; 2 to 4 calls at once on a big node (memory use, and whether two subtasks really run together); a draft model from the same family on the GPU desktop (is it faster?) and from a different family (the load should fail with llama.cpp's message); the prompt progress line on a CPU node with a long prompt.
- Chat and the `/v1` API with a real llama-server and a thinking model (reasoning folded away in Chat, `reasoning_content` in the API), and with real tools: Continue in VS Code, aider and Open WebUI pointed at `https://<controller>:8443/v1` with a chat-only token (each needs the self-signed certificate trusted), including one tool-calling request. Also check a chat waits rather than interrupting while a task keeps every node busy.
- Wake-on-LAN on the real cluster: the Wake button and the automatic wake for a waiting task, on a Linux node with `ethtool -s <nic> wol g` and the Windows GPU desktop (WoL on in the BIOS and the network adapter's power settings); a node on Wi-Fi (usually cannot be woken). Check which MACs each node reports (node card data in `/api/state`, `hello.macs`).
- Working hours on a node with a big model: outside the hours the model should stop within about a minute of the last call, and load again on the first task after the hours start. A scheduled task over a few days (daily at night) with a node planner, checking the Finished notification and that a still-running previous run causes a skip.
- Phone home-screen install (iPhone Safari and Android Chrome) against the controller's self-signed certificate: does it open full screen with the icon, or only as a bookmark? The live state stream through a reverse proxy, if one is ever put in front of the controller (it should fall back to polling within about 8 seconds).
- Screen reader pass (NVDA on Windows, VoiceOver on a Mac or iPhone): dialogs announced with their titles, task status announcements, the Ctrl+K search box.
- The catalog update from a real release: the first release built with this change carries `catalog.json`; a controller on that version or later should show "Suggested models: list from v…" on the Updates page after its next check.
