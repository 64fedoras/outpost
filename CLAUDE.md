# Outpost

Self-hosted distributed local-AI cluster, written in Go as **one binary** with two modes:
`controller` (web UI, planning, job dispatch, model and release storage) and `node` (runs
llama.cpp, runs commands, keeps no job data). Current version: **0.6.1** (`const Version` in `proto.go`).

The owner uses it to fan programming tasks out across their home machines, as a supplement to Claude Code.
You describe a task in the web UI; a planner (Claude API, or a local model on a node) splits it into
subtasks, asks questions if unclear, shows a plan to approve; nodes write the files in parallel; the
controller reviews, runs tests on an exec-enabled node, fixes failures, and offers a zip.

Read `README.md` for user-facing behaviour and `docs/ROADMAP.md` for the planned work (0.7 to 1.1,
nothing built yet, decisions pending with the owner).

## Owner preferences (standing)
- **Release numbers go up by one digit only.** After 0.9 comes 1.0, then 1.1. Never 0.10 or 0.11.
- Keep Outpost **self-contained in the binary** (without making it huge) so updates and rollbacks stay easy.
  Zero runtime dependencies on nodes except what Outpost installs itself (llama.cpp builds, bundled libgomp).
- Hands-off flow: the owner approves a plan, nodes work, the finished output is downloadable from the web UI.
- Linux machines use the login name `sysmaint`.
- The Add-node command shown in the UI must be a single paste with no scp line:
  `sudo chmod +x ~/outpost-linux-amd64 && sudo ~/outpost-linux-amd64 install node --join TOKEN`
- Update README.md for every user-visible change, and bump `Version` for every release.

## The cluster it runs on
Controller `outpost-controller01` (Proxmox VM, https://10.0.4.130:8443). Nodes: outpost-node01
(10.0.4.132, 4 cores, 4 GB), outpost-node02 (10.0.4.133, 31 cores, 173 GB, on the Dell R740),
`gaming` (Ubuntu desktop, Radeon 9070 XT), Robin-Desktop (Windows, Radeon 9060 XT 16 GB). Up to about
10 machines eventually, a couple of them Macs (Apple Silicon and Intel). Nothing here can be reached
from a cloud build box; test on these machines by hand where noted in `testing/README.md`.

## Commands
```sh
go vet ./... && go test ./...            # unit tests
sh build.sh                              # all 5 platforms into ../dist/outpost-<version>/bin
sh winres/gen.sh                         # regenerate rsrc_windows_amd64.syso after bumping Version
sh testing/e2e/run.sh up                 # full local rig with stub Claude + fake llama-server
```
`build.sh` targets: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, all with
`CGO_ENABLED=0`. Windows is deliberately built **without** `-s -w` and with version info and a manifest
(`winres/`), because stripped, metadata-free Go exes are a common Defender false positive.
`winres/gen.sh` (called by `build.sh`) needs `llvm-rc` and `llvm-cvtres` (Debian/Ubuntu package `llvm`); the generated `rsrc_windows_amd64.syso` is committed.

### Vendoring
All dependencies are in `vendor/` (fyne.io/systray, godbus/dbus/v5, golang.org/x/sys, coder/websocket).
The original build machine could not reach proxy.golang.org, so `vendor_sync.sh` re-vendors from local
git clones and patches `vendor/modules.txt`. **On a machine with normal network access just run
`go mod tidy && go mod vendor`** and ignore that script. `go.mod` lists coder/websocket as `// indirect`
although it is imported directly; harmless.

## Release routine
1. Change code; add or update tests.
2. Bump `Version` in `proto.go`; run `sh winres/gen.sh`.
3. `go vet ./... && go test ./...`; run the e2e rig for anything touching tasks, the UI or updates.
4. `sh build.sh`; copy README and source next to `bin/`; zip as `outpost-<version>.zip`.
5. The owner uploads the five binaries in the web UI's Updates tab, updates the controller, then each node.
   Rollback is a button there. The controller finds the version of an uploaded binary through the
   `OUTPOST_BUILD_INFO|<version>|` string (`buildInfo` in `proto.go`, regex in `update.go`), so `Version`
   must stay a plain string constant.

## Code map (all in package main, repo root)
| Area | Files |
| --- | --- |
| Entry, flags, TLS | `main.go` (usage, data dirs, self-signed cert, fingerprint pinning `pinnedTLS`), `proto.go` (wire types, `Version`) |
| Controller | `controller.go` (routes in `Run`, auth/sessions, node WebSocket hub, join tokens), `controller_settings.go`, `controller_jobs.go`, `controller_models.go` (HF download, storage modes, model push to nodes), `controller_runtime.go` (llama.cpp builds), `controller_update.go` (releases, node and controller updates, rollback), `admin.go` (PBKDF2 password) |
| Tasks | `tasks.go` (task model, planner, dispatch loop `executeTask`, review, test and fix, cost cap, pooling pipelines), `tasks_chat.go` (conversation: messages, questions, steering, follow-up rounds), `llm.go` (Claude API client) |
| Node | `node.go` (connect, jobs, exec, file transfer, pause, node lock), `node_llama.go` (llama-server supervision, runtime install, chat), `node_rpc.go` (ggml-rpc-server for memory pooling), `node_update.go`, `node_control.go` (127.0.0.1 control server for the tray), `hw*.go` (CPU/RAM/GPU detection, Windows via DXGI/PDH/registry, no cgo) |
| Tray | `tray.go` (loop), `tray_linux.go` (StatusNotifierItem/DBusMenu), `tray_windows.go` (Win32), `tray_icon.go` (icons drawn in code), `tray_other.go` |
| Install, update, restart | `install*.go` (systemd on Linux; Windows Run key and autostart), `update.go`, `relaunch_unix.go`, `relaunch_windows.go`, `nodelock_*.go`, `sys_*.go` |
| Web UI | `web/index.html`, `web/app.js`, `web/style.css` (vanilla JS single-page app, embedded with `go:embed web`, dark mode, sidebar shell, mobile drawer) |
| Bundled | `bundled/linux-amd64/libgomp.so.1.gz` (+ licence): needed by llama.cpp's Linux builds, missing on minimal installs |
| Test builds | `e2e_hook.go` (build tag `e2e`: `OUTPOST_TEST_HF` redirects Hugging Face); `OUTPOST_ANTHROPIC_URL` and `OUTPOST_GITHUB_API` redirect the Claude and GitHub APIs |

## Things that bit before (do not repeat)
- **`exec.Cmd.Wait` may only be called once.** Supervisors (`llamaMgr`, `rpcMgr`) own an `exited` channel
  that `watch()` closes after `Wait`; `stop()` waits on the channel. A second `Wait` hangs forever.
- **Windows cannot replace a running exe.** The node runs under a launcher: `launchNewest` and
  `runChildLoop` (`OUTPOST_CHILD=1`), child exit code 75 means quit, a different code means relaunch
  the newest version. In 0.6.0 a non-child `relaunch()` ran the child loop inside the old process, leaving
  two live nodes with the same ID; 0.6.1 spawns a fresh process and exits instead, and both modes take a
  per-data-dir lock (`acquireNodeLock`, `node.lock`). Never run the child loop inside the process that is
  being replaced.
- A node that was updated by the buggy 0.5.0/0.6.0 launcher needs a one-time manual restart onto 0.6.1.
- Pooled pipelines are cancellable (`pipelines map[string]*pipeline`); unloading a pool cancels its pipeline.
  A helper restart used to leave the main node without a model; covered by tests.
- Review prompts must not repeat the current file contents (`subtaskPromptWith(withCurrent=false)`).
- Do not use `pkill -f` in scripts that run from the same shell; match by exact executable name
  (see `testing/e2e/stop_all.sh`).
- Join codes are single use and expire; "New code" cancels the previous unused code (`handleNewToken` with `replace`).
- Playwright selectors: starter cards also contain words like "Python"; scope with `.opt >> text=...`.

## Status
- v0.6.1 is built and was verified on Linux (update, restart, lock). **Windows 0.6.0 to 0.6.1 self-update
  is untested on real Windows**; the owner was about to try it and may report `node.log`
  (`C:\ProgramData\outpost\node\node.log`). Defender can still block web-UI updates of an unsigned exe;
  the documented workaround is `Add-MpPreference -ExclusionPath 'C:\ProgramData\outpost'`.
- The tray icon exists for Linux (desktop sessions) and Windows; macOS has none yet.
- Next: get the owner's decisions on `docs/ROADMAP.md`, then build releases in order.
