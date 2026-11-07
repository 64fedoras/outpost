# Installing and first run

Installing the controller and nodes, the tray icon, and the first run.

Part of the [Outpost user guide](USER-GUIDE.md).

## Install (starts at boot)

Linux with systemd. Copy the binary to each machine, then install it as a service. Re-running an
install is safe: it updates the service and keeps existing data and passwords.

### Controller

```bash
scp outpost-linux-amd64 admin@controller.example.local:~/
ssh -t admin@controller.example.local 'sudo chmod +x ~/outpost-linux-amd64 && sudo ~/outpost-linux-amd64 install controller --advertise https://192.0.2.10:8443'
```

A first install sets **no password** and prints or logs none. Open the web UI right away, accept the
self-signed certificate warning and choose your admin password on the "Set up Outpost" screen (8+
characters); you are signed in and that screen never appears again. Whoever opens it first sets the
password, so do it straight after installing. By default only private/LAN addresses (and loopback) are
accepted for this step; to allow others add `--setup-allow-from 203.0.113.0/24,10.0.0.5` (or `any`) to
`install controller` / `controller`. Setup is rate limited and only works while no password exists.

### Nodes

**One paste (easiest).** Upload the binary for each platform you use in the **Updates** tab once. On the
**Nodes** tab click **Add node**: the top two boxes are ready-to-paste commands with the controller
address and a join code already filled in. On Linux or macOS run

```bash
curl -fsSk --pinnedpubkey 'sha256//<controller key>' 'https://192.0.2.10:8443/install.sh?t=<code>' | sh
```

and on Windows paste the PowerShell command into an **Administrator** terminal. The script works out
the platform, downloads the matching binary from the controller, checks its SHA-256 against the
uploaded release and runs `install node --join` (using `sudo` on Linux when needed). The connection
is pinned to the controller's own certificate, so no `-k` trust-on-first-use is involved. The
downloads only work with an unused, unexpired join code and only from private/LAN addresses
(widen with `--setup-allow-from`). **Untested on real Windows, macOS and the real cluster.**

**Copying the binary yourself.** Put `outpost-linux-amd64` in the node's home folder (any way you like), then on the **Nodes** tab
click **Add node** and paste the one command it gives you. If you type the node's SSH target (for
example `admin@node1.example.local`) it wraps the command in `ssh -t` so you can run it from your own
computer:

```bash
ssh -t admin@node1.example.local 'sudo chmod +x ~/outpost-linux-amd64 && sudo ~/outpost-linux-amd64 install node --join <token>'
```

The node service runs as the user who ran `sudo`, keeps its data in `~/.outpost/node`, and runs jobs
as that user. A machine that already joined becomes a service with `sudo outpost install node`.

Nodes need nothing else installed. The controller fetches llama.cpp and models once and copies
them to nodes, and the Outpost binary carries the small system library llama.cpp needs that minimal
Linux installs lack (`libgomp`), dropping it next to llama.cpp and stable-diffusion.cpp (images and video) only when the machine is missing it.
The one exception is GPU drivers: GPU nodes use llama.cpp's Vulkan build, which needs the system's
Vulkan driver (`sudo apt install libvulkan1 mesa-vulkan-drivers`). The node card tells you if it is
missing.

**Older Linux (glibc):** llama.cpp's newest Linux builds can need a newer glibc than the machine has
(for example Ubuntu 22.04 and JetPack 6 on Jetson have glibc 2.35). When a node reports
`glibc too old: this build needs glibc 2.38 or newer, this machine has glibc 2.35 (tried llama.cpp bNNNN, ...)`, the controller
automatically tries the next older llama.cpp release, up to 8 times, and remembers the one that
worked for that node. If none of the recent releases runs, the error says so; the machine then needs
a newer OS. **Untested** on a real Jetson or other arm64 boards: covered by unit tests only.

**Freeing disk space automatically:** when a node is about to receive a model (copied from the controller or downloaded from Hugging Face) and its disk is too small, it deletes stored models to make room instead of failing. It removes models the controller also holds a copy of first, then, only if that is not enough, models it downloaded straight from Hugging Face (they download again when needed); within each group the least recently used goes first (last load or arrival), and it stops as soon as the new model fits with some headroom (the larger of 1 GB and 5%). It never touches the loaded model, the one it reloads after a restart, or one being copied or downloaded, and models the controller does not know are left alone. If deleting everything eligible would still not make room, nothing is deleted and the error says so (`not enough disk space: need 16.9 GB, have 15.4 GB; ... nothing was deleted`). Each cleanup is written to the node log and shown on the node card for a day ("Freed 12.0 GB by removing X"). Turn it off under Settings -> Free disk space. The "need" figure subtracts what a partly downloaded model already has on disk. **Untested** on real nodes, Windows and macOS: covered by unit tests with a simulated disk.

**Cleaning up old models:** a node's card lists the models stored on its disk, each with a **×** to delete just that one (after a confirmation; models in use have no ×, and the node refuses them anyway). **clean up…** opens a
list with sizes, everything ticked except the model that is loaded; the button shows how much space
the deletion frees. The node itself always keeps the model that is loaded, the one it reloads after a
restart (autoload), and any model being copied or downloaded right now, whatever the request says.
*Also delete interrupted downloads* removes model folders that never finished (no manifest). Only
folders directly inside the node's `models/` folder are deleted, and symlinks are skipped. Copies on
the controller and on other nodes are untouched, so a node that needs a model again fetches it again.
The **×** next to a model still deletes just that one.

**Windows:** open an **Administrator** PowerShell (right-click Start, *Terminal (Admin)*), go to the
folder with the exe and run the command from **Add node**:

```powershell
.\outpost-windows-amd64.exe install node --join <token>
```

This installs the **Outpost Node** Windows service: it runs as LocalSystem, starts at boot with no
one signed in, restarts if it stops, and updates and rollbacks from the web UI restart it by
themselves. From a normal (non-admin) PowerShell the install stops before using the join code and
says how to open an admin one, so the same code still works. The installer also adds the Microsoft
Defender exclusion for `C:\ProgramData\outpost` (and prints how to undo it; skip with
`--no-defender-exclusion`). The tray icon starts when you sign in and talks to the service;
**Quit Outpost** there stops the service (it stays stopped until the next boot), and while it is
stopped the tray shows **Start Outpost**, which starts it again without an admin prompt (the
installer lets signed-in users start and stop this one service). Remove it with
`outpost.exe uninstall node` (admin; data and models are kept).

`--no-service` installs the old per-user mode instead: a background process with a tray icon that
runs only while you are signed in. Run the install again from an admin PowerShell (without
`--no-service`) to switch an existing per-user node to the service; its `node.json` is kept, so no
join code is needed. Either way the exe lives in `C:\ProgramData\outpost\outpost.exe` and logs go
to `C:\ProgramData\outpost\node\node.log`.

**Windows Firewall prompts.** Each update runs a new exe (`node\agent\outpost-<version>.exe`), so a
firewall rule tied to one exe path stopped matching and Windows asked "allow this app on public and
private networks" after every update. The installer, when run as administrator, now adds one rule by
port instead: **Outpost model sharing**, inbound TCP 8444, private and domain networks only, local
subnet only, for every Outpost version. A service node also converts a rule from an older install by
itself at start. A per-user install from a non-admin shell cannot add the rule, so the prompts
continue there; run the install command once in an admin PowerShell to stop them. If Windows has the
network marked **Public**, set it to **Private** (Settings, Network, the connection's properties):
the rule deliberately does not apply on public networks. Pool helper ports (pooling, off by default)
still get no rule. *Untested on real Windows.*

*Untested on real Windows:* the service, and whether llama.cpp's Vulkan backend can use the GPU
when run by a service (session 0). If a GPU node shows no GPU after switching, reinstall with
`--no-service`.

**macOS:** run `chmod +x ./outpost-darwin-arm64 && ./outpost-darwin-arm64 install node --join <token>`
(without sudo). This writes a per-user LaunchAgent (`~/Library/LaunchAgents/io.outpost.node.plist`) so the
node starts when you log in and restarts if it stops, with the binary in `~/.outpost/outpost` and logs in
`~/.outpost/node/node.log`. It is per-user, not a system daemon, so llama.cpp can use the GPU through Metal.
`outpost uninstall node` removes it. A menu bar item (see *Tray icon* below) is installed with it and starts at login.
*Untested on a real Mac.* Running `outpost node --join <token>` in a terminal still works.

**Check cluster.** Once nodes are added, **Check cluster** on the Nodes page tests every online node in one go:
a 16-token call to its loaded model, an `echo` command (if commands are on), a connection from the controller to
its file-sharing port, free disk on the models folder (warns under 10 GB), its clock against the controller's
(warns past 30 s) and its version. Each problem comes with a sentence on what it breaks and, where there is one,
a command to fix it (for example opening the firewall port or turning on time sync). Offline nodes are listed but
not tested. The check changes nothing and only the last run is kept, in memory.

### Tray icon (desktop machines)

Desktop nodes get a tent icon in the system tray (notification area): **green** while taking work,
**amber** while paused, **gray** while disconnected or stopped. Its menu:

- Status, the controller it is joined to, and the loaded model.
- **Work mode**, one of three (the ticked one is the current state):
  - **Accept new work:** the controller can send tasks and jobs here.
  - **Finish current work, then pause:** no new work; jobs already running here finish first. The
    status line says *finishing N job(s)* until they are done, then the node is paused.
  - **Pause now (cancels running work):** stops running jobs at once. A subtask that was being
    written goes back in the queue for another node, and a test run starts again on another exec
    node (up to twice). Other steps running there (review, fix, agent or build runs) fail as they
    would if the node went offline. While paused with nothing running, this item is the ticked one.

  Paused is amber, and the node card in the web UI says who paused it. Pausing survives
  restarts. Pausing does **not** unload the model, and pools keep lending.

  The same pause is on each node card in the web UI (**Nodes → List → click a node → Pause…**): finish current
  work then pause, pause now (running work moves to other nodes), or pause for 1 or 8 hours, after which the
  node takes work again on its own. **Resume** on the card ends any pause, including one set from the tray.
  Nodes older than this feature can only be paused from their tray icon; the web UI says to update them.
- **Unload model:** frees the model's memory (RAM and VRAM) now, the same way an idle unload does.
  The node keeps taking work and loads the model again when the next task needs it (pause
  it as well if nothing should load it). Greyed out while a job is running, while
  the model is still loading, and for a pooled model (unload that from the web UI). After a node
  restart the model loads again as usual.
- **Open controller web UI.**
- **Join a different controller…:** opens a small local page where you paste a join code from
  the other controller's **Add node** dialog. Models stay on disk; the old settings are kept as
  `node.json.prev`.
- **Quit Outpost:** stops the node and closes the icon. On Linux the service stays stopped until
  **Start Outpost** in the tray (asks for your password), `sudo systemctl start outpost-node`, or
  the next boot.
- **Close this icon only:** the node keeps running.

On Linux, `install node` adds the icon to your desktop login when the machine has a desktop. It is
started right away if you are logged in to the desktop, and at every login after that. GNOME needs the
AppIndicator extension (Ubuntu Desktop includes it; on other GNOME setups install
`gnome-shell-extension-appindicator` and enable it). If no icon shows, read `tray.log` in the node's
data folder: it says when nothing on the desktop is listening for tray icons. Linux servers without a
desktop get no icon. Untested on a real desktop.

On macOS the same menu appears in the menu bar. `install node` adds a second per-user LaunchAgent
(`io.outpost.tray`, logs in `tray.log` in the data folder) that starts it at login; run `outpost tray &`
from Terminal to start it by hand. There is no cgo in Outpost's builds, so the item is drawn by a small
script run through `/usr/bin/osascript` (AppKit via the JavaScript-for-Automation bridge) that the
binary embeds. **Start Outpost** there restarts the node's LaunchAgent. *Untested on a real Mac:* the
menu code is unit-tested and the macOS binaries compile, but the menu bar item itself has never run.


**Windows and Microsoft Defender.** Defender sometimes flags Outpost (a false positive common for
unsigned Go programs that download and run new versions of themselves), which breaks updates from
the web UI. Exclude Outpost's folder once, in an admin PowerShell:

```powershell
Add-MpPreference -ExclusionPath 'C:\ProgramData\outpost'   # installed exe, updates, models, llama.cpp
```

Then click **Update** again. `outpost hwinfo` prints the CPU, RAM and GPUs a machine reports, so you
can check detection without joining.

## First run

   16 GB GPU, *Qwen3-Coder 30B-A3B*, or *Qwen2.5-Coder 14B* for a tighter fit. Newer options to compare on your own
   16 GB GPU, *Qwen3-Coder 30B-A3B* (or *Qwen2.5-Coder 14B* if it is tight), *Qwen2.5-Coder 14B*. Newer options to compare on your own
   tasks (not yet tested on the cluster, and they need a current llama.cpp build): *Qwen3.6 35B-A3B* for the
   CPU node or a 16 GB GPU with RAM offload, *Qwen3.6 27B* for 16 GB GPUs, and *Gemma 4 26B-A4B* as a second
   opinion for review. These three can also read images (marked **Vision**): their vision projector file
   (`mmproj`) is downloaded with them and passed to llama-server automatically. Choose where it lives:
   - **Add · nodes download** (default): each node downloads it from Hugging Face when you load it.
     Uses no space on the controller; nodes need internet access.
   - **Store on controller:** downloaded once to the controller and copied to nodes over your
     network. Uses controller space; nodes need no internet.
   You can switch later: **Free controller space** or **Store on controller** on the library row.
   Library rows and Hugging Face file lists show a badge per node: **fits in VRAM**,
   **split VRAM + RAM (slower)**, **runs on CPU** (node without a GPU) or **does not fit**. It is an
   estimate: the weights plus a rough KV cache at the suggested context size, not read from the file.
   **Discover models** lists the built-in models as compact rows, the ones suggested for your nodes first,
   with a search box and filters (**Fits my nodes**, **Fits in VRAM**, **Vision**). Each row says how many
   online nodes it fits (hover for each node); the per-node detail is in the **Add and load on…** picker,
   and the **▾** next to it has **Add · nodes download** and **Store on controller**. On a ready model,
   **Load on N nodes it fits** loads it on every online node where it fits fully in VRAM (this replaces
   whatever those nodes had loaded). **Load on…** opens a node picker (fit per node, and which model it
   would replace). A built-in model that is not in the library yet has **Add and load on…**, which adds it
   (nodes download it) and loads it in one step. A **Recommended** tag shows which node a model is the pick for.
2. **Nodes** (sidebar): a summary row on top (online, models loaded, jobs, total speed, free VRAM,
   nodes that need attention), then three views, picked top right and remembered in this browser:
   **Grid** shows a card per node with CPU, RAM and VRAM gauges, speed, problems with a fix button
   (failed load, model error, paused, no Vulkan, low disk under 10 GB, update available) and the model with
   Change/Unload; nodes that need attention come first, and a memory pool (a node and the helpers lending
   it memory) sits in one dashed frame with **Unload pool**. **List** shows two-line rows grouped into
   Needs attention, Healthy and Offline; click a row (or **Details**) and the node's full card opens right
   below it (every control: idle unload, benchmark, stored models, tools, exec and sandbox, Pause, Restart,
   Logs, Rename, Remove, Run). An optional **Detailed list** view (a short list on the left, the full card
   and recent jobs on the right) is off by default; turn it on in **Settings → Application → Appearance**
   (kept in this browser only). **Details** on a Grid card opens the node in the List view.
   The full card shows **Recommended**: the best coding model that fits that
   node (fully in VRAM first, then VRAM plus RAM, then RAM; skipped if the node's disk is too small, or
   if no llama.cpp build exists for it), with the reason and a one-click **Load**. A node running the
   CPU build (for example a Linux node with a GPU but no Vulkan driver) is sized as a CPU machine.
   On CPU machines the pick favours speed over size: the node times its own memory (about a second,
   at start-up), the controller reads each model's GGUF header to see how many weights one token
   reads (a mixture-of-experts model reads a few percent, a dense one all of it), and the card shows
   **~N tok/s** with the reason. Under 4 tok/s it is marked slow in orange. If the node has a
   benchmark, that measured speed replaces the estimate for that model and rescales the others.
   The estimates are conservative guesses until you compare them with real benchmarks; the full
   **Load model** list still offers every model.
   Otherwise click **Load model** on a node. **Context size** is a slider (powers of two from 2K up to the
   model's trained context, or 128K when that is not known) with a box beside it for an exact number. Under it,
   the dialog shows the context cache that size needs and the total with the weights, and turns amber when that
   is more than the GPU memory, more than the node can hold, or past the trained context. The controller reads
   the model's GGUF header (layers, KV heads, head size, sliding window) when the model is added, from its own
   copy or by streaming only the header from Hugging Face; until the header is read it falls back to the rough
   estimate above. **Largest that fits** sets the biggest context whose estimate stays within the node's VRAM
   (or RAM on a CPU node, or VRAM plus RAM when the weights alone do not fit the GPU), with 10% spare because the
   estimate is not exact.
   **Context cache** shrinks the context memory: 8-bit uses about half (answers are nearly identical), 4-bit about a
   quarter (a small loss in quality), so a longer context fits; the estimate follows the choice. It needs flash
   attention, which llama.cpp turns on by itself where it can (**Advanced → Flash attention** forces it on or off).
   **Experts in system RAM** appears for mixture-of-experts models (the controller reads the expert sizes from the
   model file): *All experts* passes `--cpu-moe` to llama.cpp, *First layers only* passes `--n-cpu-moe N`. The
   attention weights and the context cache stay on the GPU, so a model larger than the VRAM can run much faster than
   with a plain spill; the hint and **Largest that fits** count the experts against RAM instead of VRAM. If the extra
   arguments already contain `--cpu-moe` or `--n-cpu-moe`, those win. Needs a llama.cpp build new enough to know the
   flags; untested on real hardware.
   **Calls at once** (2 to 4) lets a big node run that many model calls in parallel, for example writing one subtask
   while reviewing another. Each call keeps the full context size, so the cache needs that many times the memory;
   the estimate includes it, and the dispatcher gives the node that many calls. **Advanced → Draft model** loads a
   small model of the same family (same tokenizer, under half the main model's size; a fifth or less works best)
   for speculative decoding: it guesses a few tokens ahead and the main model checks them, often 1.5 to 2 times
   faster for code on a GPU. A draft from a different family makes llama.cpp refuse to start, and the load fails
   with its message. Outpost does not measure the gain by itself: compare the tok/s on the node card with and
   without it. The node card lists these options next to the speed. Outpost installs llama.cpp on the node (Vulkan for
   GPUs, CPU otherwise), gets the model onto the node, and starts it. Downloads resume if
   interrupted and are checked against Hugging Face's SHA-256. Progress shows on the card. The node
   reloads the same model by itself after a reboot or update.
   **Integrity re-check:** after a node starts on a new Outpost version it hashes each stored model once in the
   background and compares it with the SHA-256 recorded when the model arrived (a model about to load is checked
   first). A mismatch means the file is corrupt on disk: the node deletes that model and reports it, and loading
   it again fetches a fresh copy.
   **Node-to-node copies:** if another online node already holds the model, the new node copies it from up
   to 3 such nodes over the LAN (TCP port 8444, 64 MiB chunks, resumable) instead of downloading again or
   pulling it from the controller. The controller supplies the expected SHA-256, the peers' certificate
   fingerprints and a one-time token; the node pins each peer's certificate, checks every chunk and then the
   whole file, and only a match is stored. A peer that sends bad data is skipped for an hour. If no peer
   works, it falls back to Hugging Face or the controller as before. Models stored on the controller stay
   there as an optional seed; **Release to a node** (Models tab) copies a stored model to the online node with
   the most free disk and then frees the controller's copy, so that node becomes the seed. `outpost install node` opens port 8444 in ufw, firewalld or (Windows, as
   administrator) Windows Firewall when one is active. Nodes started by hand may need that port opened.
3. Tick **exec** on at least one node so tasks can run their tests.
   Exec nodes list the tools they have (Go, Python, Node, gcc, git, zip, Chromium; rescanned hourly)
   on their card. The planner is told what each node has and which shell it uses, and each test or
   setup command goes to a node that has the tool it starts with. For Go, click **Install Go** on the
   node you choose: the controller downloads the official release once, checks its SHA-256 and
   sends it; Outpost keeps it in its own data folder and puts it on the PATH of exec jobs. Other
   tools are detected, not installed. Windows exec nodes run commands in PowerShell, not `sh`.
   **Command sandbox (Linux nodes).** Commands run in a sandbox, set in Settings → *Command sandbox* and per
   node on its card (*default*, *off*, *basic*, *strict*). *Basic* (the default): a command can only
   change files in its own job folder, a private temporary folder and a shared build cache; it can read
   system folders (`/usr`, `/lib`, `/bin`, `/opt`, a few files in `/etc`) and Outpost's toolchains but not
   your home folder, SSH keys, Outpost's data or other jobs; the environment is cleaned (no tokens from
   the node; proxy and Go module settings are kept); it has no special rights and cannot write huge
   files. The network stays open. *Strict*: the same, with the network off except for steps that download
   packages (`go mod download`, `npm install`, `pip install`, `git clone`, …) and commands you type under
   **Run…**, plus a deny list for dangerous system calls. A failing command whose output looks like a
   blocked file or network access gets a note so the fixer does not rewrite working code. The node card
   shows *Sandboxed*, *Partly* or *Off*, and what is enforced when you hover. Needs Linux 5.13 or newer
   for file confinement; *strict* is refused on kernels that cannot do it, *basic* quietly does less.
   Tools installed in your home folder (`~/.cargo`, `nvm`) are hidden from commands: install them under `/usr/local`
   or `/opt`, or set `OUTPOST_SANDBOX_EXTRA_RO` (colon-separated folders) in the node's service environment.
   Test commands that start with a download step run with the network open for the whole step. If a build
   breaks and you suspect the sandbox, set the node to *off* and compare. Windows and macOS nodes run
   commands without a sandbox.
4. **Settings:** add a Claude API key if you want Claude as the planner (recommended), or an
   OpenRouter key to plan with any model OpenRouter offers.
5. **New task:** describe what to build and click **Plan it**. See below.

