# Chat, the API and cloud planners

Chat, the OpenAI-compatible API, connecting other tools, Claude and OpenRouter usage and the spending cap.

Part of the [Outpost user guide](USER-GUIDE.md).

## Chat and the OpenAI-compatible API

**Chat** (sidebar) is a plain conversation with a model on your nodes, for quick questions that do not
need a task. A new chat opens with two cards, **Auto (local, strongest free node)** and **Research**;
**Pick a specific model…** under them lists each loaded model with *Any free node* (the chat goes to whichever
node that has it is free) and a chip per node. Click a card or chip, or use the model box under the
message box. The message box stays docked at the bottom, and the **Chats** button opens a panel with your earlier
chats (on a phone it is a full-screen list). Replies stream in and are shown as formatted text (headings, bold, lists, tables, links and code blocks with a Copy button; HTML in an answer is shown as text, never run; the same goes for planner messages in a task and in the project chat), thinking
models show their reasoning folded away, and each answer notes the node, model and speed. **Enter**
sends and Shift+Enter adds a line (Settings → Appearance, or the key badge next to Send, switches to **Ctrl+Enter** sends; the choice is kept in this browser; on a phone Enter always adds a new line and the **Send** button sends; the model list shows just the chosen model with a **Change** link, and the page follows the on-screen keyboard), **Stop** ends a reply, and **Make this a task** copies the conversation into a new task. Chats are
kept on the controller (`chats/`, up to 500); a long chat sends only the newest messages that fit the
model's context. Chat only uses a free model slot: while a task keeps every node busy, a message waits
up to 5 minutes and then says so. It never interrupts task work and costs no Claude money, unless
you turn on web search below.

**The ＋ menu and skills.** The ＋ button left of the model box holds **Attach images or documents**, **Run code**
(a chip next to ＋ shows it is on; click the chip to turn it off) and the **Skills**: **Make an image** and
**Make a 3D model**. Pick a skill, describe what you want and press Send: instead of a chat message you get a
card above the box with your text, the options (model, size and count for pictures) and **Make it**, or
**More settings…**, which opens the full Images or 3D page with your text filled in. If you type something that
reads like a picture request ("draw an image of…", "make a 3D model of…"), a line under the box offers the skill;
**Not now** dismisses it. This is a plain wording check, not the model deciding.

**3D requests go to the 3D card.** A message that clearly asks for a 3D part (a make or change word such as
make, design, modify or remix, plus a 3D cue such as "3D model", "3D print", STL, 3MF, OpenSCAD, "rack mount" or a
MakerWorld, Printables or Thingiverse link, and not phrased as a question) opens the 3D card when you press Send
instead of going to the chat model. Nothing runs until you press **Make it**. **Answer in chat instead** sends it to
the chat as normal and keeps that chat in chat from then on (remembered in this browser). A question about 3D printing
("what infill should I use for a rack mount?"), a message with images or documents attached, or code ("the STL
vector") stays in chat; when your message had a 3D cue, the answer gets a **◇ Make this in 3D** button that opens
the same card. Outpost cannot edit an existing mesh: with a model link the card says so and offers a new OpenSCAD
part instead, reading the linked page and looking up the measurements of the devices you name (see
[3D models](3d-models.md)). If no node can render, the card has the one-click **Install OpenSCAD** and turns
**Make it** on when the install is done. The Images (`#/image`) and 3D
(`#/cad`) pages still exist and are in Ctrl+K search. Letting the chat load or switch models on nodes is not built.

**Faster follow-ups (prompt cache).** A node keeps the processed start of the last prompt and skips it when the next
message starts with the same text. To make that happen more often, a follow-up in an Auto chat goes back to the node
(with the same model loaded) that answered last. If that node is busy and the chat is long (about 1,000 prompt
tokens or more) it waits a few seconds, otherwise any free node answers. Long chats are also trimmed in steps of
four turns instead of one message per turn, so the start of the prompt stays the same for several messages. Under
each answer, *N of M prompt tokens reused* shows how much the node did not have to read again (*N prompt tokens, none reused* when nothing was reused or the
node's llama.cpp build does not report it, and the node's log says which; the count comes from the progress report, the usage block or the timings block, whichever the build sends). The non-streaming `/v1/chat/completions` answer carries the same number as `usage.prompt_tokens_details.cached_tokens`. The "remember this?" check that runs after early answers continues the same chat prompt, so it does not push the chat out of the node's cache. With a thinking model (Qwen3 and similar) the reply the model just wrote is not reused on the next message: its chat template drops the earlier thinking when the history is sent back, so only the prompt up to that reply is reused. Plain models reuse the whole earlier turn. **Answer length.** One chat answer may use about a quarter of the model's context (4,096 to 16,384 tokens, never more than half the context), and never more than the node can write in about 10 minutes at its measured speed, so a slow node ends an answer itself before the 15-minute chat timeout. A thinking model may spend about 55% of that on thinking; if it has not started answering by then, it is stopped and asked again to answer directly, with a note under the answer. If an answer hits the limit, Outpost carries it on by itself, up to three times (it asks the model to continue and joins the pieces; the previous thinking is passed back along with the answer so far, though whether the model's chat template keeps it depends on the model). It stops early when the context has no room left, when a round adds nothing, or when the chat has used half its 15-minute timeout; a note under the answer says how many times it carried on. If it is still cut off after that, the **Continue** button under it asks the model to carry on. If a thinking model uses the whole length limit on thinking, the answer says so instead of showing an empty cut-off. **Thinking is folded.** A thinking model's thinking is kept with its answer and shown behind a closed line above it ("Thinking… 18 s" while it thinks, "Thought for 21 s" afterwards); click the line to read it. Thinking is never sent back to the model as part of later messages. While a model works, a green bar under the answer says what it is doing: reading your message (the bar fills as the prompt is read), thinking, writing (with its speed), waiting for a free node, or running code. The cache is only a speed-up and is kept in the node's memory
only: nothing is saved to disk, and a model switch, idle unload or restart simply starts it again.

**If a node drops out while answering,** the chat carries on: once the node has been offline for about 8 seconds,
the controller drops the partial text, picks another node and answers again from the chat history it holds
(up to two other nodes), with a note under the answer. This is slower because the new node reads the whole prompt.
A document attached to a chat is cut shorter on later messages than on the first one, so the cache is
mostly missed after the message that carried the document.

**Auto picks a model for each message.** Auto works out what a message needs: **vision** when a picture is
attached, **coding** when it looks like code work (code blocks, file names such as `main.go`, words like
bug, compile, refactor), otherwise **general**. This is a few simple rules, with no extra model call, and
the answer ends with a *chosen for coding ▾* menu: change it and the message is answered again for that need.
If a loaded model suits the need (a model that is both coding and vision suits both), chat uses it and nothing
changes. Otherwise, on nodes with *Let chat switch models here* on (Nodes page, on by default), chat looks for
a node that is idle and whose model chat did not change in the last 10 minutes, and picks the cheapest way to
get a suitable model:

1. a suitable model already on that node's disk (loaded without asking, the current model is replaced);
2. a download that fits in the free space: chat asks first, with a **10 second** countdown. If nobody
   answers, a download **under 20 GB** goes ahead; a bigger one does not;
3. a download that needs room: chat lists the models it would remove and asks, with a **30 second**
   countdown. Nobody answering means **use the loaded model instead**; nothing is removed.

Removals keep one model of each type (coding, vision, general) and take from the type with the most
models first; copies the controller also holds go first, then the least recently used. At most 2 models are
removed for one message and 3 per node per day. A model used or loaded in the last 24 hours, the loaded model,
models of unknown type (custom uploads) and models the controller does not know are never removed. If that is
not enough room, chat removes nothing, answers with what is loaded and says what to do (raise the node's
storage limit or remove models on the Models page). Pinning a node or a model never switches. The prompt has
**Download now**, **Use loaded model instead** and **Cancel**. The memory of recent use and removals is
kept in the controller's memory only, so it starts empty after a restart.

**Screenshots in chats.** Paste a screenshot into the message box (Ctrl+V), drop image files on it, or
use **Attach** in the ＋ menu: up to 5 PNG, JPEG, WebP or GIF images per message, 5 MB each (bigger photos are scaled
down in the browser first). A message can be just an image ("Describe this image." is asked for you). Only a
**Vision** model can read images: one with its projector file (`mmproj`), such as Qwen3.6 or Gemma 4 from
the Models page. With **Auto** the chat goes to a free node that has one loaded; if no ready node has one
(or the node you picked does not), the message box says so and the message is not sent. The images are
saved with the chat on the controller (`chats/<id>-images/`), shown again when you reopen it, and deleted
with the chat. A node gets them only inside the request, in memory, and keeps nothing. Later messages
resend only the newest 3 images, so a long chat does not fill the model's context.

**Documents in chats.** **Attach** in the ＋ menu (or drop, or paste) also takes up to 5 documents per message, 25 MB
together: PDF, Word (`.docx`), Excel (`.xlsx`), OpenDocument text (`.odt`) and CSV. The controller reads
each into plain text when you send (a pure-Go reader, nothing to install): PDF text with `[page N]`
marks, Word and ODT paragraphs and tables, each Excel sheet as tab-separated rows under `[sheet name]`
(formulas as their last saved values), CSV as it is. The message shows a chip per document with its
pages or sheets and word count, and **Text** opens what was read. The text is kept with the chat
(`chats/<id>-docs/`, deleted with the chat, copied by **Branch**) and goes to the model with that
message every time the chat is answered, so any model can use it, not only Vision models. Limits: a
scanned PDF has no text layer and is not read as images (the chip warns "a scan?"); text past about
400,000 characters is cut (the chip says "cut to fit"); and when the conversation is longer than the
node's context window, older documents are cut first and the newest message keeps the most, with a
note to the model that the rest did not fit. Old `.doc` and `.xls` files are refused (save them as
`.docx` or `.xlsx`). Encrypted or password-protected files are untested.

**Run code.** **Run code** in the ＋ menu (per chat, off by default, remembered with the chat and
copied by **Branch**) lets the answering model run programs: Python, Go or a shell script, whichever an
online node that allows commands (exec on) can run. Python needs `python3` (or `python`) already on that
node, as for task tests; Go is the toolchain Outpost installs; shell needs a Linux or macOS node. Each
program runs as an exec job on the least busy such node, in the same sandbox as task tests (no network
in strict mode), with a 60-second limit, in an empty folder holding the chat's documents as text under
`files/`. The model sees the output (and its errors, so it can fix the program and run it again), up to
6 programs per answer. Files the program writes to `out/` come back: images (PNG, JPEG, GIF, WebP, SVG)
show under the answer and every file has a download link, up to 12 files; files over 1 MB are left
out (the model is told which). Each run shows as a card above the answer (language, node, exit
code, time; open it for the code and output). Files are kept with the chat (`chats/<id>-runs/`) and
deleted with it; the node keeps nothing. Later messages see a one-line summary of each run. Models of
about 3B and smaller, and servers without tool calling, answer without running anything and the note
under the answer says so. Untested: real models writing and fixing code in a loop, Go and shell runs on
real nodes, and Windows nodes (shell is not offered there; Python and Go should work but are untested).

**Research.** Pick **Research (across nodes)** in the chat's model box (or its card on a new chat) for a
question that needs looking up. The strongest free node splits it into 3 to 6 sub-questions; each one runs
as a lane on whichever node is free (with one node they run one after another), searching the web with
`web_search` and reading pages with `read_page`, and writes findings with their source URLs. Then a free
node writes a Markdown report with a short answer and numbered sources, streamed like any answer. While
it works the chat shows the lanes (waiting, working, done, with node and search counts); afterwards the
**Research** card above the report lists each sub-question with its findings, the note under it counts
lanes, nodes, searches and pages, and **Open report** opens it in the preview panel to download or save to
a project. **Stop** cancels every lane. Searches go to DuckDuckGo from the controller, spaced at least
1.5 seconds apart, and fall back to Brave Search when DuckDuckGo blocks and a Brave key is saved
(Settings), whatever the chat web-search setting says. Models of about 3B and smaller, or without tool
calling, get one search per lane instead. Each answer can take a while (up to 45 minutes before it gives
up). Reports from local models are drafts: check the sources. Untested: live DuckDuckGo with several lanes
(rate limits), real models, and more than one node running lanes at once.

**Editing, regenerating and branching.** Under your message, **Edit** changes it and answers again from
there; under an answer, **Regenerate** asks again, **Try on…** asks again on another node (or Auto), and
**Branch** starts a new chat with the conversation up to that point (its images and instructions come
too). Nothing is thrown away: the earlier message or answer, and everything that followed it, stays as a
version, and the **‹ 1 / 2 ›** arrows switch between versions (up to 20 per message; beyond that the oldest
goes). Only the version on screen is sent to the model. **Copy** copies a message's text.

**Instructions** (top bar) are standing rules for one chat, for example "Answer in Go. Short answers,
code first.", added to the system prompt of every message in it (up to 4,000 characters) and shown as a
strip at the top. Tick **Also use these for new chats** to make them the default; new chats then start
with them (the setting is `chat_instructions`, kept with the other settings and in backups).

**Search chats** at the top of the Chats panel filters by title as you type and finds chats whose
messages contain every word (the same search as the sidebar's "Search tasks and chats").
Drag the panel's left edge to make it wider or narrower (240 to 560 px; arrow keys work on the focused
edge, double-click resets); the width is remembered in this browser.

**Live preview.** When an answer (in Chat or the project conversation) contains a whole HTML page, an SVG
image, a Mermaid diagram (a code block marked `mermaid`) or a long Markdown document (marked `md`), a
card appears under it; **Open** shows it in a panel on the right with **Preview** and **Code**, a
version picker (blocks with the same name in one conversation are versions: an HTML page is named after
its `<title>`), **Download** and **Save to project…** (writes the file into a project you pick; refused
while a task works in that project; an existing file with that name is replaced). Task files ending in
`.html`, `.svg`, `.md` or `.mmd` get a **Preview** button in the file viewer. The preview runs in a
sandboxed frame with its own policy: the page's scripts run, but it has no cookies, cannot reach
Outpost's pages or API and cannot load anything from the network (no external scripts, images or
fonts; inline ones work). Mermaid 11 is bundled in the binary (about 1 MB, compressed) and loaded only
when a diagram is shown. **Untested:** Safari and real phones; tested in Chromium on the local rig.

**Memory** (Settings → Memory) is a list of short facts, each for everywhere or for one project, added
to the start of every chat and project conversation ("Prefers Go and plain JavaScript"). Add one by
hand, or take a suggestion: after an answer from a local model, the same node is asked once whether
your messages state a lasting fact; if so a card under the answer offers **Remember**, **Edit first**
or **Not this**. Nothing is saved without your click, a conversation gets at most one open suggestion
and stops asking after 3 answers, and paid planners never make suggestions. Facts can be edited,
moved between everywhere and a project, or forgotten; the list shows about how many tokens it adds
to each message. Up to 100 facts of 300 characters, kept in `memory.json` on the controller and in
backups. When Claude or OpenRouter answers a project conversation, that project's facts and the
everywhere facts go with the request. A task in a project is planned with the same facts (the project's and
the everywhere ones; with Claude or OpenRouter they go with the planning request); tasks outside a project
do not use memory. A project's facts are also listed, added and forgotten in the project's **Context** tab.
Two switches: **Use memory** (`memory_on`) and **Suggest new memories** (`memory_suggest`), both on by
default. **Untested:** how good real models' suggestions are; tested with a stub model.

**Web search in chats** (Settings, Tasks, Planner: "Let chats search the web", off by default). The same
switch lets the project conversation look things up (always with the built-in free search; see
using-tasks.md, *Web look-ups*). Local models cannot search by themselves, so a **Search with** menu
picks how chats get the web:

- **The local model itself** (free, no cloud key). The model gets two tools, `web_search` and `read_page`,
  and the controller runs them while it answers, for up to 4 rounds, then the model writes the answer
  with source links. Searches go to DuckDuckGo's HTML results page (no key, but unofficial: it can be rate
  limited or ask for a CAPTCHA). If a **Brave Search** API key is saved (Settings, API keys), Outpost
  falls back to Brave when DuckDuckGo fails, and skips DuckDuckGo for 10 minutes after it blocked
  us. `read_page` fetches a page and reads it as text (1 MB, 15 s, about 8,000 characters per page, less
  with a small context: at 8k about 4,000), and
  always refuses loopback, LAN and other internal addresses, whatever the pasted-links setting says.
  At most 3 tool calls run per round; older results are cut so the request fits the model's context.
  Models that cannot call tools reliably get "search once, then answer": a server that refuses tools, tool
  calls that keep failing, or a model whose file name says 3B or smaller (for example `qwen2.5-3b`, `llama-3.2-1b`). The search
  is on the chat's latest message and the results go in front of it. If every search fails the chat is still
  answered, with a note.
- **Claude** or **OpenRouter** (paid). With this on each chat message first asks a cloud model with web search to
look the question up and write a short brief with source links; the local model then answers using it.
For these two, Claude (its own search tool, about $0.01 per search, up to 3 per
message) or OpenRouter (its web search plugin on the OpenRouter model you chose, any model including Claude
Opus, roughly $0.004 per result billed by OpenRouter). Either way there is a small model call too, and it is counted
in Spending and the monthly cap. It needs that provider's API key. If there is no key,
the cap would be passed or the search fails, the chat is still answered and the answer notes why the web
was not used. When it did search, the answer notes how many searches and what it cost.

**When a setting is in the way.** If a chat or the project conversation cannot do what you asked only
because a setting is off, the answer ends with a small card: what was blocked, what turning it on does
(and costs), and two buttons. **Turn on … and try again** changes that one setting and writes the answer
again (in a chat the first answer is kept as another version; in a project the answer is replaced unless
it started a task). **Not now** closes the card, and that conversation does not offer the same setting
again. Nothing changes until you click. The model is told which features are off and marks the answer;
plain phrases such as "I can't browse the internet" also count, for small models that skip the mark.
Offered today:

| Card | Changes | Where to change it back |
| --- | --- | --- |
| Turn on web search | *Let chats search the web*, for every chat and project. With no Claude key saved, *Search with* becomes the free built-in search | Settings → Tasks → Planner |
| Turn on Run code | This chat only. Offered only when some node allows commands | The Run code switch above the message box |
| Turn on memory | *Use memory*, everywhere | Settings → Memory |

Missing API keys, a node's *Allow commands* switch and memory pooling are not offered: each needs a
choice the card cannot make for you. Picking another model already has its own prompt (*Let chat switch
models*). The cards are web UI only; API answers never carry them. **Untested:** how often real local
models add the mark (tested with a stub model).

**Export, personas and Compare.**
- **Export** (header of an open chat): *Markdown* (`.md`, with headings and code blocks), *Plain text* (`.txt`), or *Print or save as PDF*, which opens your browser's print dialog with a clean copy of the chat (choose "Save as PDF" as the printer). Images and documents are listed by name, not embedded. Nothing is sent anywhere; the file is made in your browser.
- **Personas**: in **Instructions**, pick a saved persona to fill the box, or write instructions and press **Save as persona…** to keep them under a name (saving under an existing name replaces its text). **Delete** removes a persona; chats that already use its text keep it. Personas are stored in the controller's settings and can be read and written with the `chat_personas` setting.
- **Compare** (header, shown when two online nodes have a model loaded): send one prompt to two nodes and read the answers side by side with tokens per second, token count and time, and a *faster* mark. Each node answers when it is free. Nothing is saved to a chat; use **Copy** to keep one answer. **Stop** cancels both.

**OpenAI-compatible API.** Editors and tools that speak the OpenAI API (Continue, aider, Open WebUI and
others) can use the cluster. Point them at:

```
base URL:  https://192.0.2.10:8443/v1
API key:   an API token from Settings → API tokens
model:     auto   (or the name of a loaded model, from GET /v1/models)
```

`POST /v1/chat/completions` supports streaming, `max_tokens`, `stop`, tools (tool calls are sent in one
piece at the end of a stream), image parts as `data:` URIs for vision models, and `reasoning_content`
for thinking models. A model name that no ready node has loaded returns 404; when every node is busy a
request waits up to 2 minutes for a free slot, then returns 503. Choose **Chat only** when you create a
token for a tool: such a token works on `/v1/` and nothing else. The controller's certificate is
self-signed, so the tool must trust it (or use your own certificate, see [Sign-in security](operations.md#sign-in-security-api-tokens-certificate-and-backups)).

### Connect an agent (Hermes, aider, Open WebUI and others)

An AI agent can use the cluster as its model backend, so the computing happens on your nodes:

1. Settings → API tokens → name it after the agent, choose **Chat only**, create, copy the token.
2. In the agent's settings choose an OpenAI-compatible provider and enter the base URL
   (`https://<controller>:8443/v1`, also shown under API tokens), the token as API key, and the model
   `auto` (or a name from `GET /v1/models`).
3. Revoke the token in the same place to cut the agent off.

Agents that call tools work when the model's own chat template supports tools (nodes start llama-server
with `--jinja`; `tool_choice`, `top_p`, `seed`, penalties, `response_format` and
`stream_options.include_usage` are passed through). Requests share free model slots with chat, so a busy
cluster makes an agent wait up to 2 minutes and then answer 503; agents should retry. Not offered yet:
per-token rate or spend limits. The API is plain HTTPS
with a self-signed certificate by default; keep it on your LAN, or put a trusted certificate in front
before exposing it. Not tested against Hermes or the real cluster.

### Embeddings

`POST /v1/embeddings` works like OpenAI's: `{"model": "auto", "input": "text" or ["text", ...]}` returns one
vector per input, scaled to length 1, with `usage`. `encoding_format` may be `float` (default) or `base64`
(what the OpenAI Python client asks for). Up to 512 inputs per call and 32,000 characters each; token
arrays and `dimensions` are refused. A **Chat only** token works here. `GET /v1/models` lists the ready
embedding models with `"capabilities": ["embedding"]`.

Embedding models are small models that turn text into vectors for search. Add one on the **Models** page
(*Add to library* on Nomic Embed Text, the default, or Qwen3 Embedding 0.6B for other languages; any
Hugging Face GGUF whose name says "embed" works too). They get an *Embedding* tag, have no *Load* button
and are never offered as chat or task models. When a request arrives, Outpost picks an online node that has
llama.cpp (preferring one that already has the model, then the one with the most CPU threads), copies the
model there if needed, and starts a second llama-server on the CPU just for embeddings, so the chat model
keeps the GPU. That server stops after 5 minutes without a request; nothing is kept on the node but the
model file. With no embedding model in the library the API answers 404 and says what to add. The catalog
file names and sizes for the two models were typed from the Hugging Face listings and could not be checked
from the build machine; if a download fails, use *Add a model from Hugging Face* instead.

### The Stash: documents your chats can search

The **Stash** page (sidebar) holds *stashes*: named collections of *resources* (PDF, Word, Excel, OpenDocument,
text, Markdown, CSV and code files up to 50 MB each, or text you paste). Drop files on a stash or use *choose
files*; each resource is cut into passages of about 800 characters (a page number is kept for PDFs), embedded
with the embedding model above and indexed. A stash holds up to 500 resources and 200,000 passages in all
(6,000 per resource); there can be 50 stashes.

Where things live: the original files, the passages and the vectors are all on the **controller**
(`data/stash/<id>/`). A node is only sent the passage text to embed, in memory, and sends vectors back; it
keeps nothing, and nothing goes to the cloud. The Stash is part of the encrypted backup and shows as its own
part in Settings → Storage.

Search combines meaning (vectors) with exact words (BM25) and merges the two rankings. *Try a search* on a
stash page shows what a chat would find, with the matching words marked; a hit opens the passage with its
neighbours. If the embedding model changes, the stash shows *Needs re-index* and answers by words only until
you press *Re-index*. *Advanced* sets passage size and overlap (changing them re-indexes). Without any
embedding model in the library the page offers *Add Nomic Embed Text*.

**In a chat:** ＋ → *Search a stash* and tick one or more stashes (up to 5; a chip shows them next to ＋, and
the choice is kept with the chat). Before each answer the question is searched; passages that are close enough
are numbered and put in front of the model, which is told to cite them as [1], [2] and to ignore any
instructions written inside them. The answer shows *Sources* chips (click one to read the passage) and a
folded *Searched the stash* line. If nothing is close enough the model is told so and says it. Your files are
added after any cloud web search has run, so they are never sent to Claude or OpenRouter. Research mode does
not use the Stash. Projects have their own stash for their tasks (see using-tasks.md, *The project's stash*).

Untested: real embedding models (the relevance cut-off of cosine 0.45 is a guess for Nomic and Qwen3 and may
need tuning), large PDFs and large stashes, scanned PDFs (no text means no passages; the resource shows
*failed*), and Safari, Firefox and real phones.

### The Anthropic and Ollama APIs

Some tools only speak Claude's API or Ollama's. The same API tokens work for both; Settings → Sign-in
security → **Connect a tool** shows the addresses with Copy buttons.

**Anthropic Messages API** at `https://<controller>:8443/anthropic` (the client adds `/v1/messages`;
`/v1/messages` on the root works too). The token goes in `x-api-key` or `Authorization: Bearer`. Any
model name starting with `claude-` means `auto`, so Claude Code works without changing its model names:

```
ANTHROPIC_BASE_URL=https://192.0.2.10:8443/anthropic ANTHROPIC_AUTH_TOKEN=<token> ANTHROPIC_MODEL=auto claude
```

Text, base64 images, tools (`tool_use` and `tool_result` blocks, `tool_choice`), `stop_sequences`,
`temperature`, `top_p` and streaming (Anthropic's server-sent events) are translated to the same
inference call as the OpenAI API. Thinking text from reasoning models is left out of the reply, server
tools (Anthropic's web search, code execution and so on) are ignored, and
`/v1/messages/count_tokens` gives an estimate (about 4 characters per token). When every node is busy for
2 minutes the answer is `529 overloaded_error`, which clients retry. Node.js tools such as Claude Code
need to trust the controller's certificate (`NODE_EXTRA_CA_CERTS`) unless you installed your own.
Claude Code sends a long system prompt and many tools, so only large local models do well with it.

**Ollama API** at `https://<controller>:8443/ollama`: `/api/chat` and `/api/generate` (streamed as
newline-delimited JSON unless `"stream": false`, with `options` for temperature, top_p, seed, num_predict
and stop, `format` for JSON output, images and tools), `/api/tags` and `/api/ps` (the loaded models plus
`auto`), `/api/show` and `/api/version`. A `:latest` suffix is ignored. `pull`, `push`, `create`, `copy`
and `delete` are refused, because models are managed on the Models page. The app must be able to send
`Authorization: Bearer <token>`; Ollama itself has no sign-in, so apps that cannot add a header cannot use it.

**Browser tools (CORS).** Tools that run in a browser tab need their origin listed under Connect a tool
(for example `http://localhost:3000`, one per line, at most 20). Listed origins get CORS headers on
`/v1`, `/anthropic` and `/ollama`; nothing else on the controller changes.

**Untested:** real Claude Code, real Ollama clients and real models with these APIs; tested only
against the fake node in the unit tests.

### Outpost as an MCP server (Claude Code, Claude Desktop)

`https://<controller>:8443/mcp` speaks the Model Context Protocol (Streamable HTTP, plain JSON replies, no
event stream), so an MCP client can hand work to the cluster. Create a token with **MCP only** chosen
(Settings → Sign-in security; a full-access token works too), then, for Claude Code:

```
claude mcp add --transport http outpost https://192.0.2.10:8443/mcp --header "Authorization: Bearer <token>"
```

Claude Desktop and other clients take the same URL and header in their MCP settings (**Connect a tool →
MCP** shows both). The tools:

| Tool | What it does |
| --- | --- |
| `start_task` | Starts a task (prompt, optional project by id or name, optional planner) and returns its id |
| `task_status` | Status, the planner's questions with their ids and options, the plan and subtask progress, tests, the result note |
| `answer_task` | Answers the questions (`answers`: question id → answer, plus a note), or sends a follow-up request |
| `approve_plan` | Approves the plan, only if allowed (below) |
| `get_files` | Lists the task's files, or returns one file's text (up to 200 KB) |
| `list_tasks`, `cluster_status` | Recent tasks; nodes, their models and whether they are busy |
| `ask_local_model` | One question to a local model (free) |

Two safety settings under **Connect a tool → MCP**, both off by default: **Let MCP start tasks with Claude
or OpenRouter planners** (off: only local planners, `auto` or `node:<id>`, so a tool can never spend money)
and **Let MCP approve plans** (off: plans wait for you in the web UI, and the tool is told to poll). The
tools call the same code as the web UI, so caps, project rules and the one-task-per-project queue apply.
An MCP-only token works on `/mcp` and nothing else; chat-only tokens do not work on `/mcp`.
**Untested:** real Claude Code and Claude Desktop; tested with Go tests and curl on the local test rig.

## Claude usage and the spending cap

Claude only plans, writes subtasks the local models could not finish, and fixes failing tests; all
routine code writing runs on your nodes. Every Claude call is metered live on the task page.

- **Soft cap per task** (default $2, set when you approve the plan): before a call that would pass
  the cap, the task **pauses**. Raise the cap, or finish with a local planner. Nothing is thrown away.
- **Monthly cap for all tasks**: when you save your first Claude or OpenRouter key, Outpost asks once for a monthly limit (prefilled $25; **No limit** keeps it off). Otherwise it is off by default. Set or change it on the **Spending** page (sidebar, under
  Updates). When this month's Claude and OpenRouter spend, across every task, would pass it, a task
  pauses before its next cloud call with *Paused at the monthly cap*. Raise the cap, finish that task
  with a local planner, or leave it: it continues on its own when the next calendar month starts
  (the controller's time zone). Tasks on local planners keep running. The per-task cap still applies too.
- **Backstop:** use a separate API key for Outpost and set a monthly spend limit on it in the
  Claude Console.

### Spending page

**Spending** in the sidebar (or *Spending* in Ctrl+K search) shows, per month: the total and how much
of the monthly cap it uses, how many tasks used a cloud planner, the average per task, the Claude and
OpenRouter split, a bar chart per day, and the most expensive tasks (planner, calls, tokens in and out,
cost; click a row to open the task). The figures come from a small ledger, `spend.json` on the
controller, which every Claude and OpenRouter call adds to as it happens. Deleting a task keeps its
cost on the page, marked *deleted*, so the month's total stays right. The first time a controller with
this page starts, it fills the ledger from the tasks on disk, counting each task's whole cost on the day
it was created (earlier per-call times were never recorded), so days before the update are approximate.
The ledger keeps 24 months, is part of backups, and is in the controller's time zone.
The **Local models (free)** tile counts what your own nodes answered this month (answers, tokens in and out, how many prompt tokens were reused from cache, and the average tokens per second). It comes from a separate small file, `local-usage.json`, that keeps per-day totals for 24 months and is also part of backups. It starts empty when you update, so earlier answers are not counted.
- The key is stored encrypted on the controller and never sent to nodes.

### Web lookups

- **Pasted links:** http(s) links in your messages (up to 5 per round) are fetched by the controller
  as plain text (1 MB read, 12,000 characters kept, 15 s limit) and given to the planner as reference
  material. Links to loopback, LAN, link-local and other internal addresses are refused, including
  through redirects, unless you tick *Allow pasted links to reach internal addresses* in Settings.
- **Planner web search (off by default):** tick *Let the planner search the web* in Settings.
  The Claude planner may then make up to 3 searches per planning call, charged about $0.01 each and
  counted against the task's spending cap. Not available with OpenRouter or a local planner.

### OpenRouter

With no Claude key saved, the default planner behaves as **Auto** (a local model first, then OpenRouter if a key is set), so New task works without setup.

**Planner: Auto** (New task → Change options, or Settings → Default planner) plans on the ready node
with the biggest loaded model and records that node on the task. It needs at least one node with a model ready.

OpenRouter works as a second cloud planner and does the same jobs as Claude (planning, writing
subtasks the nodes could not finish, fixing failing tests).

1. **Settings → OpenRouter:** save your API key (`sk-or-…`), then choose a model. The box suggests
   models from OpenRouter's live list, with prices per million tokens. Use a separate key and set a
   credit limit on it at openrouter.ai.
2. Pick **Planner: OpenRouter** on a new task (or make it the default planner in Settings).
3. Spend uses the cost OpenRouter reports for each call, so the live meter and the per-task cap
   work exactly as for Claude. If a call reports no cost, Outpost prices it from the model list.

