# Chat, the API and cloud planners

Chat, the OpenAI-compatible API, connecting other tools, Claude and OpenRouter usage and the spending cap.

Part of the [Outpost user guide](USER-GUIDE.md).

## Chat and the OpenAI-compatible API

**Chat** (sidebar) is a plain conversation with a model on your nodes, for quick questions that do not
need a task. Pick a node or leave it on *Any node* (the strongest free one). Replies stream in, thinking
models show their reasoning folded away, and each answer notes the node, model and speed. **Ctrl+Enter**
sends, **Stop** ends a reply, and **Make this a task** copies the conversation into a new task. Chats are
kept on the controller (`chats/`, up to 500); a long chat sends only the newest messages that fit the
model's context. Chat only uses a free model slot: while a task keeps every node busy, a message waits
up to 5 minutes and then says so. It never interrupts task work and costs no Claude money.

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
request waits up to 2 minutes for a free slot, then returns 503. Tick **Chat only** when you create a
token for a tool: such a token works on `/v1/` and nothing else. The controller's certificate is
self-signed, so the tool must trust it (or use your own certificate, see [Sign-in security](operations.md#sign-in-security-api-tokens-certificate-and-backups)).

### Connect an agent (Hermes, aider, Open WebUI and others)

An AI agent can use the cluster as its model backend, so the computing happens on your nodes:

1. Settings → API tokens → name it after the agent, tick **Chat only**, create, copy the token.
2. In the agent's settings choose an OpenAI-compatible provider and enter the base URL
   (`https://<controller>:8443/v1`, also shown under API tokens), the token as API key, and the model
   `auto` (or a name from `GET /v1/models`).
3. Revoke the token in the same place to cut the agent off.

Agents that call tools work when the model's own chat template supports tools (nodes start llama-server
with `--jinja`; `tool_choice`, `top_p`, `seed`, penalties, `response_format` and
`stream_options.include_usage` are passed through). Requests share free model slots with chat, so a busy
cluster makes an agent wait up to 2 minutes and then answer 503; agents should retry. Not offered yet:
embeddings, per-token rate or spend limits, and CORS for browser-based agents. The API is plain HTTPS
with a self-signed certificate by default; keep it on your LAN, or put a trusted certificate in front
before exposing it. Not tested against Hermes or the real cluster.

## Claude usage and the spending cap

Claude only plans, writes subtasks the local models could not finish, and fixes failing tests; all
routine code writing runs on your nodes. Every Claude call is metered live on the task page.

- **Soft cap per task** (default $2, set when you approve the plan): before a call that would pass
  the cap, the task **pauses**. Raise the cap, or finish with a local planner. Nothing is thrown away.
- **Monthly cap for all tasks** (off by default): set it on the **Spending** page (sidebar, under
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
- The key is stored encrypted on the controller and never sent to nodes.

### Web lookups

- **Pasted links:** http(s) links in your messages (up to 5 per round) are fetched by the controller
  as plain text (1 MB read, 12,000 characters kept, 15 s limit) and given to the planner as reference
  material. Links to loopback, LAN, link-local and other internal addresses are refused, including
  through redirects, unless you tick *Allow pasted links to reach internal addresses* in Settings.
- **Claude web search (off by default):** tick *Let the Claude planner search the web* in Settings.
  The Claude planner may then make up to 3 searches per planning call, charged about $0.01 each and
  counted against the task's spending cap. Not available with OpenRouter or a local planner.

### OpenRouter

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

