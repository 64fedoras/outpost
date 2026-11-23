# Known limits

What Outpost does not do yet, and what is untested.

Part of the [Outpost user guide](USER-GUIDE.md).

See also [SECURITY.md](../SECURITY.md) for the security model and its limits (the Linux-only command sandbox, unsigned binaries and checksums).

- One model per node at a time; each node runs one model call at a time unless it was loaded with
  **Calls at once** above 1. A node lending memory
  to a pool runs no model of its own.
- Nodes need this Outpost version for agent mode: update nodes before using it with them.
- Outpost installs Go, Chromium and OpenSCAD on Linux and Windows (64-bit Intel/AMD) nodes only; on a Mac or an Arm machine they are detected, not installed. The Chromium and OpenSCAD downloads come with no publisher checksum (see install.md). The installs and PowerShell test runs are untested on real Windows.
- Screenshots need a Chromium or Chrome that can run headless on the node. A snap-packaged Chromium may not
  be able to read Outpost's work folder, and a screenshot over 1 MiB (a very tall page) is not collected.
- The low-level job history (Home → Recent jobs) is kept in memory and clears on restart; tasks
  and their files are kept on disk.
- Service install: systemd on Linux, the Outpost Node service on Windows (admin shell) and a per-user
  LaunchDaemon (root system service) on macOS, because macOS blocks login items from the local network.
  The macOS menu bar item is built but has never run on a real Mac; whether Metal works from the system
  service is untested.
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


## Linux arm64 on Ubuntu older than 24.04 runs CPU only

llama.cpp's Linux arm64 releases need glibc 2.38 (Ubuntu 24.04). A node on an older system (for example a Jetson on JetPack 6, Ubuntu 22.04, glibc 2.35) gets Outpost's own CPU-only compatible build instead (see install.md). There is no GPU/Vulkan build for it; use Ubuntu 24.04 or newer (JetPack 7) for that. The compatible build is made by a CI workflow after each llama.cpp release, so a brand new release can take up to a week to reach those nodes; the node keeps its current build meanwhile. Intel Macs without AVX2 likewise use a compatible build and run slower than newer Macs.
