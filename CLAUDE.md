# Outpost

Self-hosted distributed local-AI cluster, written in Go as **one binary** with two modes:
`controller` (web UI, planning, job dispatch, model and release storage) and `node` (runs
llama.cpp, runs commands, keeps no job data). Current version: **0.8.0** (`const Version` in `cmd/outpost/proto.go`).

The owner uses it to fan programming tasks out across their home machines, as a supplement to Claude Code.
You describe a task in the web UI; a planner (Claude API, or a local model on a node) splits it into
subtasks, asks questions if unclear, shows a plan to approve; nodes write the files in parallel; the
controller reviews, runs tests on an exec-enabled node, fixes failures, and offers a zip.

Read `README.md` (short overview) and `docs/USER-GUIDE.md` (index of the topic pages `docs/install.md`, `using-tasks.md`, `cluster.md`, `chat-and-api.md`, `operations.md`, `known-limits.md`: full user-facing behaviour) and `docs/ROADMAP.md` for what was built (rows 1 to 23, all
shipped by 0.8.0) and the notes on what is untested.
`docs/ARCHITECTURE.md` explains the data folders, node connection, task lifecycle and update flow.

## Owner preferences (standing)
- **Version numbering:** patch numbers are automatic (see below); only a **major** bump must be deliberate (owner's words, 2026-10-28: he only cares that major version numbers are deliberate). Minor bumps are by hand in `cmd/outpost/proto.go` but need no special sign-off. Two-digit components (0.8.10, 0.10.0) are fine at the owner's say-so (2026-10-28), so versions are always compared numerically, never as strings. The 0.8.0 pin was a one-time request for that single combined release.
- Keep Outpost **self-contained in the binary** (without making it huge) so updates and rollbacks stay easy.
  Zero runtime dependencies on nodes except what Outpost installs itself (llama.cpp builds, bundled libgomp).
- Hands-off flow: the owner approves a plan, nodes work, the finished output is downloadable from the web UI.
- Linux machines use a single admin login (kept in the private project artifact, not in the repo).
- The Add-node command shown in the UI must be a single paste with no scp line:
  `sudo chmod +x ~/outpost-linux-amd64 && sudo ~/outpost-linux-amd64 install node --join TOKEN`
- Update the matching topic page under `docs/` (and `README.md` only if the short overview changes) for every user-visible change. **Patch and minor versions are automatic**: every push to `main` is built as the next patch after the latest `v*` tag (0.8.0 -> 0.8.1 -> 0.8.2), and after patch 19 the minor rolls (0.8.19 -> 0.9.0, 0.9.19 -> 0.10.0), stamped at build time by `.github/next-version.sh` and `OUTPOST_VERSION` in `build.sh`, with nothing committed back. A hand-made minor bump needs no sign-off either; only a **major** bump (1.0.0, 2.0.0) must be deliberate, at the owner's say-so. PRs must not bump `Version` for a patch. `const Version` in `cmd/outpost/proto.go` is only the base: to move to a new minor or major, change it by hand, and the next build uses it if it is higher than the next patch.

## The cluster it runs on
The owner's own cluster: one controller VM, a few Linux nodes (a small 4 GB test VM, a large-RAM CPU server),
a Linux GPU desktop and a Windows GPU desktop, up to about 10 machines eventually, a couple of them Macs
(Apple Silicon and Intel). Names, addresses, login and hardware are kept out of this public repo, in the
private project artifact "Outpost home network layout" (https://claude.ai/artifact/2N8RKWgm4G5TZePPmbBXJ5).
Nothing here can be reached from a cloud build box; test on these machines by hand where noted in
`testing/README.md`. Use example values (192.0.2.x, `example.local`, `admin`) in docs, UI text and tests.

## Commands
```sh
sh testing/run_all.sh                    # vet, unit tests, JS syntax, five-platform build (also: race, e2e, all)
go vet ./... && go test ./...            # unit tests only
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
The original build machine could not reach proxy.golang.org, so `scripts/vendor_sync.sh` re-vendors from local
git clones and patches `vendor/modules.txt`. **On a machine with normal network access just run
`go mod tidy && go mod vendor`** and ignore that script. `go.mod` lists coder/websocket as `// indirect`
although it is imported directly; harmless.

## Release routine
1. Change code; add or update tests.
2. Only for a minor/major release: set the new base in `Version` in `cmd/outpost/proto.go` and run `sh winres/gen.sh`. Otherwise leave it.
3. `go vet ./... && go test ./...`; run the e2e rig for anything touching tasks, the UI or updates.
4. `sh build.sh`; copy README and source next to `bin/`; zip as `outpost-<version>.zip`.
   GitHub Actions (`.github/workflows/build.yml`) does this on every push to `main`: it runs vet and
   tests, builds the five binaries (unsigned), uploads them as a workflow artifact, and creates release
   `v<version>` with the zip and binaries the first time a version appears. It bumps nothing.
5. The owner uploads the five binaries in the web UI's Updates tab, updates the controller, then each node.
   Rollback is a button there. The controller finds the version of an uploaded binary through the
   `OUTPOST_BUILD_INFO|<version>|` string (`buildInfo` in `cmd/outpost/proto.go`, regex in `cmd/outpost/update.go`), so `Version`
   must stay a plain string constant.

## Code map (package main lives in `cmd/outpost/`; file names below are relative to it unless marked repo root. `transfer/` and `internal/` packages and the root embed package `outpost` compile into the same binary)
| Area | Files |
| --- | --- |
| Entry, flags, TLS | `main.go` (usage, data dirs, self-signed cert, fingerprint pinning `pinnedTLS`), `proto.go` (wire types, `Version`) |
| Hardening | `controller_auth.go` (lockout, sessions, API tokens), `controller_tls.go` (certificate swap), `backup.go` (encrypted backup, restore, nightly copies), `service_windows.go` (Windows service) |
| Controller | `controller.go` (types, routes in `runController`), `controller_api.go` (persistence, UI state, tokens, node settings, jobs), `controller_nodehub.go` (join, node WebSocket hub, message handling, offline sweeper), `controller_settings.go`, `controller_jobs.go`, `controller_models.go` (catalog, HF download), `controller_models_nodeops.go` (load/unload on nodes), `controller_models_store.go` (list, resolve, storage modes, release), `controller_models_pool.go` (pooling orchestration), `controller_runtime.go` (llama.cpp builds), `controller_update.go` (releases, node and controller updates, rollback), `admin.go` (PBKDF2 password) |
| Tasks | `agent.go` (agent mode: tool loop, fixer, investigate subtasks), `tasks.go` (task model and persistence), `tasks_http.go` (handlers, repo listing, zip), `tasks_calls.go` (model calls, cost cap, node selection), `tasks_plan.go` (planner), `tasks_dispatch.go` (dispatch loop `executeTask`), `tasks_fix.go` (test and fix rounds), `tasks_review.go` (peer review), `tasks_chat.go` (conversation: messages, questions, steering, follow-up rounds), `llm_alias.go` (names for the `internal/llm` types and functions used across main), `llm_openrouter.go` (OpenRouter key and model-list handler) |
| Node | `node.go` (connect, jobs, exec, file transfer, pause, node lock), `node_llama.go` (paths, downloads, llama-server supervision, chat), `node_runtime.go` (runtime install), `node_models.go` (model files), `node_llama_idle.go` (idle unload), `node_llama_util.go` (autoload, checks, model fetch), `node_rpc.go` (ggml-rpc-server for memory pooling), `node_update.go`, `node_control.go` (127.0.0.1 control server for the tray), `hw*.go` (CPU/RAM/GPU detection, Windows via DXGI/PDH/registry, no cgo) |
| Tray | `tray.go` (loop), `tray_linux.go` (StatusNotifierItem/DBusMenu), `tray_windows.go` (Win32), `tray_icon.go` (icons drawn in code), `tray_other.go` |
| Install, update, restart | `install*.go` (systemd on Linux; Windows Run key and autostart), `update.go`, `relaunch_unix.go`, `relaunch_windows.go` |
| Web UI (repo root) | `web/index.html`, `web/style.css` and plain script files loaded by `<script>` tags in order, no build step, no modules: `app.js` (helpers, sign-in, routing, polling; loaded first and shared by the rest), then one file per page or area (`new-task.js`, `chat.js`, `thread.js`, `nodes.js`, `models.js`, `updates.js`, `settings.js`, `dialogs.js`, `tools.js`, `images.js`) and `boot.js` last (it starts the UI). They share globals, so a new file must be added to `index.html` in the right place (`web_scripts_test.go` checks that); embedded with `go:embed web`, dark mode, sidebar shell, mobile drawer |
| Packages | root `outpost` package (`embed.go`, `embed_linux_amd64.go`: embeds `web/`, `LICENSE`, the notices and libgomp, because `go:embed` cannot reach upward), `internal/gguf` (GGUF header parser and `Shape`, used by the controller as `ModelShape`; `gguf_controller.go` reads headers from the model store or Hugging Face), `internal/llm` (Claude and OpenRouter clients, prices, request/response and tool types, file-block parsing; takes the key as an argument and streams text to a small `Sink` interface that the live view implements), `internal/sandbox` (exec sandbox: policy, Linux Landlock/seccomp child, Windows Job Object; the node re-runs its own binary as `sandbox-child`, dispatched in `main.go`, and `sandbox_alias_test.go` has the `TestMain` that lets main's tests do the same), `internal/platform` (per-OS helpers: free disk, process groups, `osInterrupt`, Windows RAM, the `node.lock` file lock, the Windows firewall rule text; `hw*.go`, the launcher in `relaunch_*.go` and the firewall migration stay in main because they use wire types and the update code), `transfer/` (peer file server) |
| Bundled (repo root) | `bundled/linux-amd64/libgomp.so.1.gz` (+ licence): needed by llama.cpp's Linux builds, missing on minimal installs |
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
- v0.8.0 is one combined release: roadmap rows 5 to 23 (the features originally grouped as 0.8, 0.9, 1.0 and 1.1) on top of 0.7.3, kept under 0.8.0 at the owner's request. Gaps and deviations are listed in `docs/ROADMAP.md` ("0.8.0 build notes"). **Everything in it is untested on the real cluster**, and the Windows (PowerShell test runs, Go install) and macOS (LaunchAgent) parts are untested on real machines. New code is in `routing.go`, `bestofn.go`, `tasks_autoresume.go`, `tasks_rounds.go`, `tasks_live.go`, `notify.go`, `projects*.go`, `attachments.go`, `model_fit.go`, `controller_peers.go`, `node_peer.go`, `transfer/`, `install_launchd.go`, `toolchain.go`, `controller_toolchain.go`, `controller_files.go`, `node_artifacts.go`, `codeindex.go`, `agent.go`, `tasks_images.go`, `visual.go`, `instructions.go`, `weblookup.go`.
- v0.7.3 adds OpenRouter as a second cloud planner (`internal/llm/openrouter.go`); the live OpenRouter API is **untested** (no key in the build environment), only a stub (`OUTPOST_OPENROUTER_URL`).
- v0.7.1 sends task work to the node with the biggest loaded model first and gives fix rounds memory of earlier rounds plus test-named files first.
- v0.7.0 adds rows 1 to 4 of the roadmap (see `docs/ROADMAP.md`); the Windows service, Defender exclusion and signing are **untested on real Windows**.
- v0.6.1 was built and was verified on Linux (update, restart, lock). **Windows 0.6.0 to 0.6.1 self-update
  is untested on real Windows**; the owner was about to try it and may report `node.log`
  (`C:\ProgramData\outpost\node\node.log`). Defender can still block web-UI updates of an unsigned exe;
  the documented workaround is `Add-MpPreference -ExclusionPath 'C:\ProgramData\outpost'`.
- The tray icon exists for Linux (desktop sessions) and Windows; macOS has a LaunchAgent but no menu bar icon (needs cgo and a Mac).
- Next: the owner tries 0.8.0 on the cluster and reports what breaks.
