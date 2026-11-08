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
`gpu, fast, office`. The Nodes page then shows a filter row with one button per label. A task can ask for
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


## Saved load settings and yielding the GPU

**Saved settings.** In the Load dialog, *Save these settings…* stores the model, context size, GPU layers, cache
type, flash attention, draft model, calls at once, experts-in-RAM choice and extra arguments under a name for
that node (up to 20). A *Saved settings for this node* list then appears at the top of the dialog; picking one
fills the form, and *Delete* removes it. Pool helpers are not part of a saved setting.

**Yield GPU to other programs** (per node, off by default; on the node card). On a desktop that is also used
for games, the model is unloaded when the node is not answering anything and the GPU stays more than 35% busy
for 30 seconds, and loaded again after five minutes below 10%. While unloaded the card says *GPU in use* and the
controller sends the node no work. It uses the utilisation the node already reports, so it cannot tell a game
from any other program, and image generation on the same node counts as another program. The reading comes
from the hardware sample (amdgpu sysfs on Linux, performance counters on Windows); neither has been tried
with a real game.
