# Nodes working together

Peer review, routing, best of N, memory pooling, labels, working hours and Wake-on-LAN.

Part of the [Outpost user guide](USER-GUIDE.md).

## Nodes working together

There are two ways for nodes to share one task.

### Peer review (on by default)

When a node finishes a subtask, a *different* node checks it against the plan: names, signatures,
imports, file paths and obvious bugs. It answers "OK" or sends corrected files. Each subtask
on the task page shows who reviewed it and whether it was fixed. Reviews catch mistakes before
the test run, so fewer Claude fix rounds are needed. Turn it off per task under **Change options**
on the new-task form.

A review never holds up or fails a task. It is skipped when only one node has a model loaded,
when no other node frees up within 2 minutes, or when the reviewer's answer looks broken (for
example, a rewrite much shorter than the original).

### Benchmarks and smart routing

When a model finishes loading, the controller runs a short benchmark on it (about 1,000 tokens in,
256 out) and the Nodes tab shows the speed. Real subtasks then update that figure as a rolling average.
Each model also has a **tier**, Small, Medium or Large, guessed from the parameter count in its
name (under 9B, under 25B, larger); change it in the Tier column on the Models tab.

The planner tags every subtask `easy`, `normal` or `hard`, and the plan card lets you change the tag
before you approve. Hard subtasks go to the strongest free model and wait up to 2 minutes for it
before taking any free node. Easy ones go to the fastest free node. Normal ones take the best mix of
tier and speed. Reviews prefer a node at least as strong as the one that wrote the code.
Pooled models are not benchmarked: a pool reads prompts across the network, so a test there is slow and says little about one node, and it would keep chat waiting. The same goes for a node that is lending memory.

The benchmark runs once for each node and model, when the model is ready; it is not repeated on
restarts or updates. Use **Re-run benchmark** on the node card to measure again. Small CPU-only nodes
(8 threads or fewer) get a shorter test, and so does a node whose benchmark failed; a failed or timed-out
run shows its reason on the card and is retried every 10 minutes. Real subtasks blend into the speed
figure either way (70% old, 30% new).
The benchmark and the routing have only been tested against stubs, not on a real cluster.

### Best of N on spare nodes

When a hard subtask starts and two or more other nodes with a model are idle, with no ready subtask
waiting for them, up to two of them write the same subtask too. The copies are not written into the
task at first. Each one that has a quick syntax check is run through it on an exec node
(`python -m py_compile` for `.py`, `node --check` for `.js`, `gofmt -e` for `.go`, and a JSON
validity check; other file types have no check) and is dropped if it fails. If more than one
copy is left, a free node acts as judge, compares them against the subtask's instructions and picks
one. The task log and the subtask line say which node won and why. If the judge is not available
or gives no usable answer, the copy from the strongest node is kept. The usual peer review of the winner follows.

It never takes a node from other work: extra copies start only on nodes nothing is waiting for, and
they give up their node as soon as a ready subtask finds none free. The primary copy waits at most
3 minutes for the others. Only local nodes take part, so it adds no Claude cost, but the judge step
makes hard subtasks a little slower. Choose **Off**, **Hard subtasks** (default) or **All subtasks**
under **Best of N on local nodes** in Settings. Tested against stub nodes only, not on a real cluster; the syntax check
needs an exec-enabled node with the language installed and is skipped otherwise.

**Comparing the versions.** When two or more versions finished, the subtask line on the task page gets a
**Compare N** button. It shows the versions side by side, one column per node with its model and speed, and a tab
per file. The judge's pick and the version now in the project are marked, a version the syntax check dropped
says why, and lines that the version in use does not have are highlighted. Once the task has finished or been
stopped, **Use this version** writes that version's files into the task and runs the tests (and fixes) again. Undo
on the latest request puts the earlier files back. Subtasks that build on this one were written against the
earlier version, and later fixes to these files are replaced, so check the test result. The versions are kept with
the task on the controller (`tasks/<id>/rounds/<n>/bestof/`) and are deleted with it. Tasks that ran before this
change have no saved versions. Tested with stub nodes and a faked comparison in the UI, not on a real cluster.

### Pooling memory (experimental, off by default, trusted LANs only)

Pooling is **off by default**. Read the security model below before switching it on; it is meant for a
home or lab network where you trust every device. To enable it, open **Settings > Memory pooling**,
tick **Allow memory pooling** and save. Only then does **Pool memory from other nodes** appear in
**Load model**.

For a model too big for any single machine, open **Load model** on the node that should run it
and pick helpers under **Pool memory from other nodes**. The size check counts the whole pool's
RAM and VRAM. Helpers lend memory through llama.cpp's RPC mode:

- Helpers stop their own model while lending. Their card says **Lending memory to X**.
- Every machine in the pool runs the same llama.cpp build; Outpost installs it.
- A helper listens on its pool port (50052 or the next free one) **only while a pool is running**, and
  closes it when the pool ends. With pooling off, no node starts the RPC server or opens that port, and
  the installer does not open it in any firewall (only the model-transfer port is opened).
- Pools come back on their own: if a helper or the main node restarts or reboots, the controller
  loads the pooled model again once everyone is online (usually within 15 seconds).
- **Unload** on the main node ends the pool and frees the helpers. Switching pooling off in Settings
  dissolves every pool, unloads the pooled models and tells all nodes to stop lending.
- **Upgrading:** if a pool was already configured when you upgraded, pooling is left on so it keeps
  working, and Settings shows a notice. Every other install starts with pooling off.

**Security model for pooling.** llama.cpp's RPC protocol has no authentication and no encryption.
Outpost keeps the RPC server on `127.0.0.1` and puts a small gatekeeper in front of it that accepts
connections only from the main node's IP address and logs every refused connection. That is all the
protection there is:

- The allow-list is a source-IP check. On a hostile network, a device can spoof or take over that address.
- Everything that crosses the pool (model weights and activations) travels in clear text on your LAN.
- Anyone who gets past the check can make the helper allocate memory and run tensor operations.

So use pooling only on a network you trust, never across the internet or an untrusted Wi-Fi, and keep
the pool port closed to everything else with your firewall. A tunnel over the nodes' authenticated TLS
connection is planned (roadmap row 24) but not built.

Expect slower generation than a single machine. Every token crosses your network, so wired
gigabit or faster is best. Use it for models that would not fit otherwise, not for speed.

## Labels, working hours and Wake-on-LAN

**Labels.** On a node's card (Nodes → List, click the node) click *edit* next to Labels and type tags such as
`gpu, fast, office`. The Nodes page then shows one filter button per label (in the filter bar once the cluster has 8 nodes or more). A task can ask for
a label (Change options → *nodes*), and a project can set one for all its tasks: writing, peer review and
best-of-N judging then use only nodes with that label. The planner node and the nodes that run tests are
not filtered.

**Working hours.** *set hours* on the node card picks days and a time window (22:00 to 07:00 runs past
midnight, on the controller's clock). Outside its hours a node takes no new model calls or test runs (a
call already running finishes) and its idle unload drops to 10 seconds, so its model stops soon and the
memory is free. The model loads again on the first call once the hours start. Chat and the `/v1` API
leave it alone too. The card shows *Off hours* while it is outside them.

**Wake-on-LAN.** Nodes report their network cards' MAC addresses. An offline node that has reported one
gets a **Wake** button. With *Settings → Wake sleeping nodes* on, the controller also wakes offline nodes
(inside their hours, with the task's label if any, at most once per 10 minutes each) when a task is
waiting for a worker. Wake-on-LAN must be on in the machine's firmware and OS, and the controller must be
on the same network segment: the packet is a broadcast (to `255.255.255.255` and the node's last /24) and
does not cross routers.


## Last-hour charts and GPU temperature

The node's details (click its row on the Nodes page, or *Details*) show a *Last hour* box with one row per
value: the chart, the latest value and the range. Points are 30-second averages of the heartbeats; speed is the
measured benchmark speed of the loaded model, so it shows a gap while no model is loaded.

The history is kept in the controller's memory only. Nothing is written to disk and nodes keep nothing, so a
controller restart or update starts the charts empty again.

**GPU temperature** is read on Linux from the GPU's hwmon sensor (AMD and Intel drivers). It shows next to the
GPU on the card and as a chart, and turns red at 85 °C. On Linux NVIDIA cards are read with `nvidia-smi` (installed with the driver). Windows and
macOS report no temperature, and the row is left out. Untested on real GPUs so far.

**GPU speed check.** Some GPUs are slower than the CPU next to them (old or small cards, or a backend that lacks
the features llama.cpp's fast kernels need). Before the first model load on a new llama.cpp build, or after the GPUs
change, a node with a discrete GPU times the smallest model it has (one that fits in 60% of its biggest GPU) with
`llama-bench`: once all on the GPU and once all on the CPU. This takes about a minute, once, and the load shows
"checking GPU speed". If the GPU generates under 90% of the CPU's speed, loads on that node run on the CPU (an explicit
GPU layer count in the Load dialog is kept), Pick best stops counting its VRAM, and the node's details say
"GPU slower than CPU here". Otherwise the details show both speeds, and Pick best uses the measured ratio to estimate
models split between VRAM and RAM. **Re-check** measures again on the next load, for example after a driver update.
The check is skipped (and says why) when the build has no `llama-bench` or no stored model fits the GPU; then the node
behaves as before. Apple Silicon is not checked: its GPU shares memory with the CPU and is always the faster one.
Untested on real GPU nodes so far.

**CUDA on NVIDIA (Windows).** Outpost's GPU build is Vulkan, which runs on any card. A Windows node whose GPUs are all
NVIDIA, with a driver of 551.61 or newer (read from `nvidia-smi`), also tries llama.cpp's CUDA 12.4 build once: after
Vulkan has been timed by the speed check, the next load installs the CUDA build (the controller downloads it with its
`cudart` runtime libraries and sends them as one archive; a few hundred MB, once), times it the same way, and keeps
whichever generates faster. CUDA has to win by at least 5%; if it does not start, fails its timing or is not faster, the node goes
back to Vulkan. The timings are kept per GPU set and driver, so this happens once, and again only after a driver or
GPU change. If the CUDA build cannot be downloaded or installed, the node stays on Vulkan and does not retry for a
day. There is no setting. Machines with an NVIDIA and another make of card keep Vulkan, which drives every card, and
Linux keeps Vulkan (upstream publishes no Linux CUDA build). Untested on real NVIDIA hardware and on Windows.

**Integrated GPUs (Intel iGPU, AMD APU).** A node with no discrete GPU but an Intel or AMD integrated GPU (under 1 GB of
dedicated memory) used to be treated as a plain CPU machine. It is now detected, listed apart from the real GPUs, and
gets the Vulkan build (on Linux this needs the Vulkan loader and Mesa drivers, as for any GPU). The speed check times a
small model on it the same way, using a model up to a quarter of the node's RAM, since an iGPU works in system RAM. The
iGPU is used only when it generates at least as fast as 90% of the CPU: otherwise, and until it has been measured,
models run on the CPU, as before. Its shared memory is never counted as VRAM; when it wins, Pick best multiplies the
CPU speed estimate by the measured gain (capped at 3x, since it shares the CPU's memory bandwidth). A machine that also
has a discrete GPU does not use its integrated one (llama.cpp's Vulkan backend skips integrated GPUs when a discrete
one is present), but the node card still lists it as "Integrated GPU: ... not used". This includes a desktop Ryzen's
iGPU with a 2 GB carve-out next to a bigger card, which earlier versions did not list at all. A node that moves to the Vulkan build downloads it once. Untested on real
Intel or AMD integrated graphics and on Windows.

**Missing GPU drivers (Linux).** If a node has a GPU, discrete or integrated, that llama.cpp would use through Vulkan
but its maker has no Vulkan driver installed (no matching file in `/usr/share/vulkan/icd.d`), the card shows a "GPU
driver" warning and the details say which GPU is affected and what to install (`mesa-vulkan-drivers` for AMD and Intel,
the vendor driver for NVIDIA). An integrated GPU is named even when a discrete card exists, since Outpost ignores it
then but you may want it working. The check only reads the driver files: it does not prove Vulkan can use the GPU once
they are installed. Windows is not checked (its graphics drivers include Vulkan). Untested on real hardware.

**GPUs on Intel Macs** (for example the AMD FirePro cards in a 2013 Mac Pro) are read with `system_profiler` and
listed in the node's details. An Intel Mac with a discrete GPU gets a llama.cpp build with Metal (Outpost's AVX-only
`macos-x64-compat-metal` build on CPUs without AVX2), and the GPU speed check decides whether it is used. llama.cpp's
Metal backend drives one GPU, so only the biggest card counts as VRAM. On a 2013 Mac Pro (FirePro D700, Metal 2) the
GPU measured about 6 times slower than the CPU, so such a Mac runs models on its CPU. If the Metal build keeps failing to
start, the node switches to the CPU-only build until it restarts. Untested on other Macs.

## Saved load settings and yielding the GPU

**Saved settings.** In the Load dialog, *Save these settings…* stores the model, context size, GPU layers, cache
type, flash attention, draft model, calls at once, experts-in-RAM choice and extra arguments under a name for
that node (up to 20). A *Saved settings for this node* list then appears at the top of the dialog; picking one
fills the form, and *Delete* removes it. Pool helpers are not part of a saved setting.


**Big contexts on a GPU.** If the KV cache for the chosen context would take more than half the GPU memory (for example settings saved on a CPU-only run, then a GPU is added), the node keeps the cache in RAM (`--no-kv-offload`) when RAM can hold it, and otherwise lowers the context to fit; the node log says so. Choose `--no-kv-offload` yourself under extra arguments to skip this.
The three switches below are **on by default**, for new nodes and for any node that never had the switch saved. A switch you turned off yourself after this default arrived stays off; one you left off on an older version reads as never saved, so check it.

**Let tasks change my model** (per node, on by default; on the node card). A task approved with
*Pick models for each node* may load this node's best-fit model before work starts and put the previous
one back afterwards (see [using-tasks.md](using-tasks.md)). Off, tasks only use the model you loaded.

**Let chat switch models here** (per node, on by default; on the node card). With **Auto** in chat, a node with
this on may load a model for the message when none that is loaded suits it (see
[chat-and-api.md](chat-and-api.md)). Off, chat only uses models already loaded, except that when Auto finds a suitable model it could load on a node with this off, chat asks once (**Allow on this node**, **Allow on every node**, or **Not now**) and the answer turns the setting on. It never removes a model or starts a
download without asking you first.

**Yield GPU to other programs** (per node, on by default; on the node card). On a desktop that is also used
for games, the model is unloaded when the node is not answering anything and the GPU stays more than 35% busy
for 30 seconds, and loaded again after five minutes below 10%. While unloaded the card says *GPU in use* and the
controller sends the node no work. It uses the utilisation the node already reports, so it cannot tell a game
from any other program, and image generation on the same node counts as another program. The reading comes
from the hardware sample (amdgpu sysfs on Linux, performance counters on Windows). On Linux, NVIDIA cards are read with `nvidia-smi`, so it works there only when that tool is installed. Neither reading has been tried with a real game.
