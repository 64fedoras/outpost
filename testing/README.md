# Testing

## Run everything
One entry point, `sh testing/run_all.sh`:

| Command | What it runs |
| --- | --- |
| `sh testing/run_all.sh` (or `fast`) | `go vet`, all unit tests, `node --check` on every `web/*.js`, and a build for all five platforms. |
| `sh testing/run_all.sh race` | Unit tests with the race detector (needs gcc). |
| `sh testing/run_all.sh e2e` | Starts the local rig, runs the browser scripts below (`steer`, `edit`, `rerun`, `taskmenu`, `mobile`, `dropzone`), stops the rig. Needs Python Playwright; it stops with an error if that is missing instead of skipping. `E2E_FULL=1` adds `flow.py` (about 4 minutes) and `soak.py`. |
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
