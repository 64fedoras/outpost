# Known limits

What Outpost does not do yet, and what is untested.

Part of the [Outpost user guide](USER-GUIDE.md).

See also [SECURITY.md](../SECURITY.md) for the security model and its limits (the Linux-only command sandbox, unsigned binaries and checksums).

- One model per node at a time; each node runs one model call at a time unless it was loaded with
  **Calls at once** above 1. A node lending memory
  to a pool runs no model of its own.
- Nodes need this Outpost version for agent mode: update nodes before using it with them.
- Go is the only tool Outpost installs (Chromium is detected only, never installed); the Go install and PowerShell test runs are untested on real Windows.
- Screenshots need a Chromium or Chrome that can run headless on the node. A snap-packaged Chromium may not
  be able to read Outpost's work folder, and a screenshot over 1 MiB (a very tall page) is not collected.
- The low-level job history (Home → Recent jobs) is kept in memory and clears on restart; tasks
  and their files are kept on disk.
- Service install: systemd on Linux, the Outpost Node service on Windows (admin shell) and a per-user
  LaunchAgent on macOS. The macOS menu bar item is built but has never run on a real Mac.
- macOS 15 and later need the Local Network permission for the node's LaunchAgent (see install.md). The
  Outpost binary is not signed, so macOS may ask again after each update.
- Files over 1 MiB travel between the controller and exec nodes through the node's file-sharing port
  (8444), which the controller must be able to reach. A failed build does not start fix rounds.
- Agent fix rounds cost more per round than the old single request, because the conversation is sent
  again on every step.
- Vision projector (`mmproj`) file names and sizes in the model catalog are assumptions; check them
  the first time you download a Vision model. The Claude web search tool name and its price are
  unverified against the live API, and so are OpenRouter's web plugin (planner and chat
  search) and its per-result price.
- Chat web search with the local model (built-in DuckDuckGo scrape, Brave failover, `read_page`) is tested
  only against stubs: live DuckDuckGo and Brave behaviour, real models calling the tools, the cluster, Windows
  and macOS are untested. DuckDuckGo's page layout is unofficial and may change; CAPTCHA pages are detected
  and answered with the Brave fallback when a key is saved.
- Idle unload is covered by unit tests but its reload timing is **untested with a real llama-server** on
  the cluster. A request that wakes an unloaded model waits for the whole load (large models take minutes),
  and a node restart reloads a model that was unloaded for idleness.
- Wake-on-LAN, working hours and schedules are tested with unit tests and the local test rig only; no
  machine was actually woken. "Only when the PC has been idle" is not built; working hours are clock times.
- Chat and the `/v1` API are tested against a fake node only, never with a real llama-server, a real
  editor plugin or a thinking model. Streamed tool calls arrive whole at the end, not piece by piece.
- Version 0.8.0 is **untested on the real cluster** (nodes, GPUs, real transfer speeds), on real Windows
  (service, PowerShell test runs, Go install, Defender) and on a real Mac. It was tested against stub
  Claude and OpenRouter servers and fake llama-server and Chromium. The live OpenRouter API was never
  called.


## Linux arm64 needs Ubuntu 24.04 or newer

llama.cpp publishes its Linux arm64 binaries built on Ubuntu 24.04 (glibc 2.39), and has done so since they were first offered, so stepping back to an older release does not help. A node on an older system (for example a Jetson on JetPack 6, Ubuntu 22.04, glibc 2.35) shows "glibc too old" when llama.cpp is installed. Outpost stops after the first try instead of downloading older builds, and remembers the answer for ten minutes. Upgrade the node to Ubuntu 24.04 or newer (JetPack 7 for a Jetson), or build llama.cpp from source. Linux x64 nodes still step back to an older release automatically.
