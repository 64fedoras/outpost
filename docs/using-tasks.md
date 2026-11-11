# Using tasks

The web UI, working with tasks, agent mode, saved and scheduled tasks, and projects.

Part of the [Outpost user guide](USER-GUIDE.md).

## Using the web UI

- **Search (Ctrl+K, or Cmd+K on a Mac)** opens a box that jumps to any task, node, project, page or
  Settings section, and runs actions such as *Check cluster*, *Add node*, *New chat*, a saved task or a
  theme. Type a few letters, use the arrow keys and Enter. The *Search* link at the bottom of the
  sidebar opens it too.
- **Theme:** Settings → Display and alerts → Appearance picks light, dark or the system's setting, kept in this
  browser.
- **On a phone:** add the page to the home screen (Safari: Share → Add to Home Screen; Chrome: menu →
  Add to Home screen) and it opens full screen with the Outpost icon. With the controller's self-signed
  certificate some browsers only make a shortcut, not an installed app. There are no push notifications
  from the app itself; use ntfy (see Notifications).
- **Live updates:** the page gets node and task changes pushed over one stream from the controller
  instead of asking every 2 seconds. If the stream does not get through (some proxies hold it back), the
  page goes back to asking every 2 seconds on its own.
- **Keyboard and screen readers:** dialogs take the focus when they open, keep Tab inside, and give it
  back when they close (Esc closes). Task status changes (Plan ready, Done, Failed) are read out by
  screen readers. Text colours meet WCAG AA contrast in both themes.

## Working with tasks

The web UI is laid out like a chat app. The sidebar lists your tasks in three groups: **Needs you**
(amber dot), **Running** (green) and **Earlier**; a task in a project shows the project name on its
second line. Pinned tasks sit above the groups. **Projects** sits right under **New task** at the top of the sidebar, styled the same way.

Right-click a task in the sidebar (or press its **⋯** button, which is always shown on a phone) for
**Open in new tab**, **Rename**, **Pin to top**, **Copy prompt**, **Copy link**, **Duplicate as new
task** (fills the new-task box with the same prompt and options: planner, Claude model, cap, peer review, mockups-first, tests-first, fix rounds, test time limit, project and node label; attachments and pasted images are not copied, and a planner, project or label that no longer exists falls back to the current choice), **Stop** (while it
runs), **Try again** (when it failed or was interrupted) and **Delete** (asks first; removes the
task's conversation and files from the controller, nodes keep none). From the keyboard, focus a task
and press Shift+F10 or the menu key for the menu, F2 to rename, or Delete to delete.

On a phone, the task page has one top bar with the menu button in it. Cluster pages (Nodes, Models, Updates, Settings) are at
the bottom of the sidebar. Settings is one long page in nine sections: Tasks (planner, running tasks,
project instructions, command sandbox), API keys (Claude, OpenRouter, Hugging Face), Models (idle unload, memory
pooling, model scorecard), Nodes (health colours, Wake-on-LAN, free disk space), Pictures, Display and
alerts (appearance, notifications), Updates, Security (password, sign-in, HTTPS certificate) and Storage and backup (storage, backup,
config file, diagnostics). The list on its left follows your scroll position and jumps to a section when clicked.

Each task is a conversation:

1. **You describe the task.** The line under the box says what *Plan it* will use (for example
   "Claude Sonnet 5.5 · cap $2 · peer review on"). **Change options** opens the planner, Claude model,
   spending cap, peer review, mockups-first, fix-round and test-time-limit controls.
2. **The planner may ask questions** when the request leaves open a choice that changes the design
   (language, storage, interface). Click the answers, type your own, or skip any. It asks at most
   once per request; clear requests go straight to a plan.
3. **The plan appears as a card.** Approve it, adjust the cap, or reply with changes ("use SQLite
   instead", "drop the web UI") and it revises the plan. Nothing runs until you approve.
   The card also says roughly how long the first pass will take, for example "about 9 min on 4
   nodes (writing 8 min, tests 1 min)", and each subtask shows "~3 min on gpu-desktop". The estimate
   assumes each subtask goes to the node where it would finish first (hard ones only to the strongest
   model), uses each node's measured speed, and expects as much output as the files' current size
   (or a guess by difficulty for new files), plus about 20 seconds per call and one minute for a test run.
   Fix rounds after failing tests are not included. A node that has not been benchmarked yet counts
   as slow (8 tokens a second) and the estimate says *rough*.
4. **Progress is live.** A strip fills in as subtasks finish, with which node wrote and checked each. Each running subtask has a "live output" pane showing what the model is writing; it starts collapsed, click to expand. An expanded pane stays open and keeps its scroll position, and keeps showing the last call's output (marked "finished") until the next call starts, so it no longer goes blank between the fixer's calls. The pane names the node (and "checking" during a review) and says "waiting for output" until the first token; a thinking model's reasoning is shown there too. While a node is still reading a long prompt, its pane header says how far it has got, for example "reading the prompt: 7,420 of 18,950 tokens, about 2 min left", so a slow CPU node does not look stuck (needs a llama.cpp build that reports prompt progress; older builds just show "waiting for output").
   While the tests run and the fixer works, a **Tests and fixes** card under the subtasks lists each step: the test run and which node ran it with a count such as "4 failing of 12" (when the runner prints one), what the fixer read, ran and edited, and how long the current step has been going. If the node is offline or paused, or a model has produced no text for two minutes, a warning says so instead of the card looking busy. If the planner's node restarts or is updated in the middle of a fix round, the controller waits (up to 10 minutes) for it to come back and repeats that round without using one up. The "The controller restarted, so this task resumed automatically" line in the thread appears only after the controller restarts; a node restart shows up on this card instead.
   A running subtask expands to show the text its node is writing, with tokens per second, and
   planning with Claude or OpenRouter streams the same way. The page also updates as soon as the
   task changes instead of waiting for the next refresh. Live text is kept in controller memory only
   (the last 64 KB per call) and is gone when the call ends.
   You can **steer while it works**: a message like "keep the output sorted" goes to every subtask,
   review and test fix that starts after it. You can also steer **while the plan is being made**
   (any planner: Claude, OpenRouter, a node, or Auto). Your note waits in a **queue above the
   message box** (remove it with the x). By default the planner is not interrupted: when its
   current answer is in, it gets that answer back with your note in the same conversation and
   revises it, so nothing it has done is thrown away. **Send now** in the queue instead stops the
   planner's current call, drops its unfinished answer, and plans again with your note; a
   cancelled Claude or OpenRouter call is not added to the cost meter (the provider may still
   bill the tokens it had written). Notes sent while it waits at the spending cap reach it the
   same way once the cap is raised.
5. **The result card** lists the files, the test outcome, and a **Download .zip** button. If the
   plan has a **build command** (for example `sh build.sh`) and **artifact patterns** (for example
   `dist/*`), the build runs on an exec node after the tests pass and its outputs appear on the card.
   Download them one by one, as `artifacts.zip`, or together with the source. A failed build does not
   fail the task; the card shows the build output and the source stays available. If the outputs
   include `outpost-<platform>` binaries (the names `build.sh` produces), the card offers **Publish to
   Updates**: each binary is checked (same hash as the build, a real Outpost binary whose platform matches
   its name) and uploaded as the release for that platform, like the Updates tab's upload.
   Updating the controller and nodes stays your click there; a task never updates anything by itself. The Project files
   view has checkboxes and **Zip ticked files**.
   A line under **Finished** sums the request up: how long it took, which nodes wrote it, how many
   subtasks needed another try, the fix rounds, the cloud cost and roughly how many lines changed.
   **.patch** next to **Changes** downloads that request's changes as a git diff (`git apply` takes it),
   and **All changes .patch** downloads everything Outpost changed in the task as one diff.
   Files of any size up to 2 GB per job move between the controller and exec nodes in hash-checked
   pieces (project files over 1 MiB go to the build node, build outputs come back). A node holds a
   build's output only until the controller has fetched it, and the node's file-sharing port (8444) must
   be reachable from the controller for files over 1 MiB.
6. **Follow-ups build on the same code.** Send another message after a task finishes ("add a --top N
   flag") and the planner plans only the files to add or change; workers get the current version
   of each file they change. Claude spend and the cap carry across follow-ups.

**Images and screenshots.** Click **🖼 Image** in the composer (or paste or drop up to 5 PNG, JPEG, WebP
or GIF images) to show the planner a screenshot of a bug or a design to match. They are kept with the task
(not in the zip), sent only to the planner, and shown in the conversation. Claude always accepts images;
an OpenRouter model must list image input (Outpost refuses a text-only one); a local planner needs a
**Vision** model loaded (Qwen3.6 or Gemma 4 from the catalog, or a custom Hugging Face repo where you pick
its `mmproj` file). Without one the composer warns and the controller refuses with a clear message instead
of dropping the image. Images can be attached while planning and in follow-ups, not as notes while work
is running. Workers and reviewers do not see images; the planner's plan carries what matters.

**Screenshots, visual review and mockups.** When a task writes web pages, an exec node that has Chromium or
Chrome renders them at desktop (1280 px) and phone (390 px) width and the screenshots appear in the
conversation, before the tests run. If the planner can read images (Claude, an OpenRouter vision model, or a
local Vision model) it then looks at the screenshots against your request and, if something is cut off,
unreadable or scrolls sideways on the phone, rewrites the files and the page is rendered again (at most 2
rounds; each is a planner call and counts against the spending cap). Pages come from the plan's
`preview_page`, the HTML files the round wrote, or a running app (`preview_command` plus a
`http://127.0.0.1:…` `preview_url`, Linux and macOS nodes only). If no exec node has Chromium the thread
says so and nothing else changes; install it on a node (for example `sudo apt install chromium`) and the
Nodes page will list it. Tick **mockups first** under **Change options** on the new-task page to have the planner write 2 or 3 design
variants (`mockups/a.html`, `b.html`, `c.html`, written in parallel by different nodes) and show them side
by side; **Build this one** starts a follow-up round that builds the real code from that variant, or reply
to ask for different variants. Screenshots are kept with the task, not in the zip; nodes keep nothing.

**Which node gets the work.** Subtasks go to the free node with the biggest loaded model first, and
reviews to the next biggest, so a small test model only gets work when the bigger nodes are busy.
To keep a node out of tasks altogether, unload its model on the Nodes tab.

**Idle unload.** A node stops its model after it has served nothing for an hour, so the RAM or VRAM
comes back. Change the wait in Settings → Idle unload, or per node with **change** on its card (empty =
use the Settings value). Type a time such as `30s`, `5m`, `1hr`, `1.5h` or `2d` (10 seconds to 365 days; a number
needs a unit), or `0` (also `never`) to keep models loaded. A bad value is refused with a message. The card
shows the wait that applies and marks a node whose model was unloaded for idleness as **idle, unloaded**;
the model is still offered for tasks, and the next request loads it again, so that request waits for a
normal model load. Unloading it yourself with **Unload** is different: the node then has no model and
gets no work until you load one. A pooled model is never unloaded for idleness.

**Handing off to a coding assistant.** When local models get most of the way but not all of it,
**Hand off to a coding assistant** on the result card downloads a zip with the files plus
`HANDOFF.md` (the requests, the plan, what each request changed, the test command, the last failing
test output and next steps) and `outpost-changes.patch`. The zip also has a short pointer to
`HANDOFF.md` in each assistant's own instructions file, so whichever you open it in picks it up:
`CLAUDE.md` (Claude Code), `AGENTS.md` (OpenAI Codex and other agents that read it), `GEMINI.md` (Gemini
CLI), `.github/copilot-instructions.md` (GitHub Copilot) and `.cursor/rules/outpost-handoff.mdc`
(Cursor). If the project already has one of those files, its text is kept and the pointer is added at
the end. Unzip it, open the folder in the assistant and ask it to read `HANDOFF.md`. Nothing is sent
anywhere by Outpost; the zip is only a download.

**Redo one subtask.** When a task has stopped, **Redo…** on a subtask row writes just that subtask
again and then runs the tests (and fix rounds) again; the other subtasks and their files are kept.
Choose who writes it: the strongest free node, a particular node (it waits up to 10 minutes for that
node to be free, then lets any node take it), or the planner (a cloud planner counts against the
spending cap). An optional note ("use argparse, not click") is added to that subtask's instructions
for this try only.

**Editing a file by hand.** On a task that has stopped, open a file in the Project files view and press
**Edit**, change it, and **Save**. Your edits become their own step in the thread ("You edited 2
file(s) by hand"), so **Changes**, **.patch** and **Undo** work on them like on any request, and the
next follow-up plans on your edited files. Only text files up to 1 MB can be edited this way.

**Deleting and renaming files.** A plan can now delete files and rename (move) them, listed on the plan
card before you approve. They are applied before the subtasks run, kept like any other change, and
shown as "deleted" in **Changes**; **Undo** brings them back. A plan cannot delete or rename a file
that one of its own subtasks writes.

**When a local planner's plan is not valid JSON**, the next try asks llama-server to keep its answer
to the plan's shape (a JSON schema). Only the retry is constrained, so models that plan well today are
not affected; an older llama.cpp build that does not take a schema simply answers without it.

**Fix rounds and the test time limit.** By default the planner gets up to 3 fix rounds and one test run
may take 10 minutes. Change both per task under **Change options** (1 to 8 rounds; 5 to 60 minutes), or
as a project default in the project's settings (⚙ on the project page).

**When the tests fail**, the planner gets its fix rounds (3 unless changed). Each round sees the test output and
which files earlier rounds changed, and works as an agent (below). It treats the tests as the
specification and only edits a test that is clearly wrong.

**Unsaved changes and drafts.** If a Settings card, the plan editor, the message editor or the new-project
form has unsaved changes, leaving the page (a sidebar link, the browser's back button, closing or
reloading the tab) asks first: **Stay**, **Discard changes**, or **Save and leave** (plan and message
editors only; Settings cards each keep their own Save button). Password, key and token fields are ignored.
Text you type in the new-task box, a task's reply box and the "Something else…" answer boxes is kept as a
draft in this browser only (never sent to the controller or the nodes), comes back when you return, and is
cleared when you send it, delete the task, or press **Discard draft**. Drafts older than 30 days are dropped.

### Agent mode

Fix rounds and `investigate` subtasks get tools instead of one-shot answers:
`list_files`, `read_file` (whole or a line range), `search` (regular expression), `write_file`,
`run` and `ask_user`. The agent reads the failing test, opens the file it points at, changes it and
runs the tests again, the way you would at a terminal. Each run is limited to **30 tool calls and
20 minutes** (time spent waiting for your answer does not count); at the limit the agent is asked
for a short summary and the round continues with whatever it changed.

- **`run`** executes a command on an exec-enabled node with a fresh copy of the project, on a node
  that has the tools the command names (see the Nodes tab). Nothing persists between runs, so the
  agent chains steps with `&&`. With no exec node the agent is told so and reads the code instead.
- **`ask_user`** posts a question in the thread with optional buttons. Only that subtask waits;
  other subtasks keep going. A fix round has the whole task waiting. If the controller restarts
  while a question is open, resuming the task starts that step again.
- **Investigate subtasks.** When a request needs digging first, the planner may add up to two
  subtasks with `kind: investigate`. A worker node reads, searches and runs commands (it cannot
  write files) and posts a short report in the thread; subtasks that depend on it get the report in
  their prompt. If the worker cannot do it, the planner does.
- **Which models.** Fix rounds use the task's planner: Claude (tool use), OpenRouter (the chosen
  model must support tools) or a local model. Investigations use the worker node's model. Local
  nodes now start llama-server with `--jinja` so the model's own chat template handles tool calls;
  the model needs to support tool calling (many recent coding models do; check the model card). If a
  planner refuses tools, the fix round falls back to the old single request, and a model that
  answers with `<<<FILE>>>` blocks instead of calling tools still gets its files applied.
- **Cost.** The conversation is sent again on every step, so a long agent run costs more than a
  single request (the Claude system prompt and the conversation so far are cached). The spending
  cap is checked before every step. Ordinary subtasks are still written in one request each.
- **Turn it off** in Settings (**Agent mode**): fix rounds and the planner go back to single
  requests, and the planner stops proposing investigate subtasks.

**Large projects.** When a follow-up's files are too big to paste into the planner's window, the
planner gets an index instead: each file's path, size, first comment line and top-level
functions and types (read with Go's own parser for `.go`, simple patterns for JS, Python and CSS).
It can ask once for the files it wants by name. Workers get the full content of the files they
change and only the declarations of files they depend on. Put a `.outpostignore` in the project
(like `.gitignore`: `#` comments, `!` to re-include, `dir/`, `*.log`) to keep folders such as
`vendor/` or `dist/` out of prompts; they stay in the project for builds and tests. `.git/` and
`node_modules/` are always left out. The one-line summary is the file's first comment, not yet written by a node.

**Stop** halts a task and keeps its files; a later message continues from there. **Activity** shows
the controller's full log for the task. **Files** opens a file tree with line numbers and light
syntax colouring, and a download button per file. Before each request writes a file, the controller
keeps the previous copy (`tasks/<id>/rounds/<n>/`). A result card's **Changes** button shows a
unified diff of that request against the files as they are now, and **Undo this request** (latest
request only, once the task has stopped) puts the files back and removes files the request added.

**Editing a message.** Hover one of your messages and click the pencil to edit it in place, like in
Claude. Before the work starts (the planner is asking questions or the plan awaits approval) any of
this request's messages can be edited: saving plans again from there. After the task has stopped, the
message that opened a request can be edited: saving undoes that request and every later one (files put
back from the kept copies, as in Undo) and plans the edited text as a new request. A running task has
to be stopped first. The old messages, plan and result stay in the thread, dimmed, and the edited
message carries an *edited* mark. Cloud spend already incurred stays counted against the cap. Edits to
a project's already-exported git work are not reverted.

**Editing and re-running a plan.** The plan card has a pencil. It opens the plan for editing: title,
summary, commands, build outputs, and each subtask's title, files, dependencies, instructions and
difficulty, with move up/down, delete and add. The plan is checked like a planner's plan (unique ids, no
cycles, safe paths, no empty subtasks). Before approval, *Save and approve* starts it; the replaced plan
stays in the thread, dimmed, and the new one is marked *edited*. On a finished, failed or stopped task,
*Re-run plan* (or *Save and re-run* after editing) puts back the files the last run wrote and runs the
plan again from the start as a new request, without planning again; the earlier run stays above. *Retry
failed subtasks* keeps finished subtasks and their files and runs only the failed ones (and the tests)
again. *Try again* on a failed task does the same for a task that stopped mid-run: failed subtasks go back
to pending and run again, finished ones are kept. Nothing can be edited or re-run while the task is running. Cloud spend keeps counting against the
cap. Design-variant (mockup) plans cannot be edited.

### Project instructions

Settings has a **Project instructions** box for standing rules, like a `CLAUDE.md` ("vendor all
dependencies, no cgo, keep the web UI vanilla JS"). The text is copied into each task when you create it
and goes at the top of every planner, worker, reviewer and fix prompt of that task. If the task's files
contain an `OUTPOST.md` at the root, its text is added as well. Both together are cut at 8,000 characters
so a small local model keeps room for the code. Changing the box affects new tasks only.

A project has its own **Instructions** box too (**Files** on the project page). For a task in that project they are added
after the Settings rules (the same 8,000-character limit applies to all of it together).

**Learning from failures.** When a task in a project finishes with its tests still failing, the planner
is asked once for a short general rule that would have avoided it ("use the testing package only; do
not add testify"). The rule appears under the project's instructions (**Files** on the project page) as a suggestion: **Add to instructions** or
**Dismiss**; nothing changes until you choose. The last 5 suggestions are kept. With Claude or OpenRouter
this is one small call counted in the task's cost, and it is skipped if it would pass the cap.

### Tests first, context warnings and the model scorecard

- **Tests first** (an option chip on the new-task form, first round of a new project only): the planner is told to
  make the first subtask write only the test files and to give a test command. Outpost then makes every other
  subtask depend on that one, so each worker is given the tests it must satisfy. If the plan has no test-only
  subtask or no test command, the planner is asked again. Saved tasks remember the option.
- **Context warnings:** when a worker's prompt had to be cut to fit the model's context window, the task log
  now says so (once per subtask), and before the first model call it lists subtasks whose existing files alone
  will not fit. The fix is a bigger context on the Nodes page; Outpost does not split the subtask for you.
- **Model scorecard** (Settings): per model and language, how many subtasks it wrote, how many were *clean*
  (accepted on the first try, not changed by the reviewer, and the task's tests passed without a fix round),
  retries and reviewer changes. Counted when a task finishes. It is for your information; routing does not use it.

## Saved and scheduled tasks

**Save for later** (next to *Change options* on the New task page) keeps the request and its options
(planner, project, peer review, fix rounds, test limit, node label) as a saved task. Attached files and
images are not kept. Saved tasks are listed under the starters: **Run now** starts one as if you typed
it, **Edit first** puts it in the box to change, **Schedule…** runs it every day or every week at a time
(the controller's clock).

A scheduled run starts on its own: the planner is told nobody is watching and asks no questions, and the
plan is approved without you. You get the usual *Finished* or *Failed* notification. To make sure a
schedule never spends money, it must plan on a node (a node or *Auto*); a schedule with Claude or
OpenRouter as the planner is refused. A run is skipped (and noted on the saved task) while the previous
run is still going, and runs missed while the controller was off are not made up.

## Projects, attachments and git

**Projects** (sidebar → Projects) keep a folder of files between tasks. Pick a project in the
composer and the task starts from the project's current files and writes into them, the way a
follow-up round does. Only one task works in a project at a time; later ones show *Waiting for
project* and start when the first finishes. A project can store defaults (test command, peer review, fix rounds, test time limit) and its own instructions.
Tasks without a project work as before, and existing tasks stay as they were. Delete a
project's tasks before deleting the project.

**The Projects page** shows one card per project with its counts (waiting on you, working, idle,
resolved); **＋ New project** opens the form in a dialog. Click a card to open the **project page**
(`#/projects/<id>`):

- **The middle** lists every request made in the project, oldest first, each followed by its task's
  card: status, the subtask progress strip while it works, the result note when it is done, and
  **Answer** / **Review plan** / **✓ Resolve**. The box at the bottom starts a task in the project with
  the project's own defaults; **More options…** opens the full new-task form with the project chosen.
- **Threads** (top bar; the amber number is how many tasks wait on you) opens a panel on the right
  with the project's **Artifacts** (screenshots, build outputs, result zips and the last pull request;
  **View all** lists them all) and its tasks in four groups: **Waiting on you** (questions, a plan to
  approve, a cap or a resume), **Working** (including tasks queued behind the one that works),
  **Idle** (finished, failed or stopped) and **Resolved** (folded; click to open). On a narrow window
  the panel slides over the page; on a phone it fills the screen. The wide-screen choice is remembered.
- **Resolving** a task (✓ on its row, **✓ Resolve** on its card, **Mark resolved** in the sidebar's
  task menu, or **Resolve all** on Idle) only tidies it away: nothing is deleted, the card leaves the
  middle (a link shows the hidden ones) and the task moves to Resolved. A follow-up, a retry or
  anything that needs you brings it back to the other groups by itself; **Reopen** does it by hand.
- **Files** shows the project's instructions (and suggested rules) and its file list; **⚙** holds the
  project's defaults, its git settings and **Delete project**; **.zip** downloads the files.

**Attachments:** the 📎 button in the composer takes up to 50 MB in total. A `.zip` becomes the
task's starting files (one top-level folder is stripped). Other files must be text and are shown
to the planner as context. Images and other binary files are refused for now.

**Git:** give a project an HTTPS (`https://…`) or SSH (`ssh://…` or `git@host:path`) URL and the
controller clones it (needs the `git` command on the controller VM, `sudo apt install git`). Each
finished round is committed to the branch you choose (default `outpost`, created if missing),
with your request as the commit message, and pushed if *push* is on. An HTTPS token or an SSH
private key is stored encrypted with `secret.key`. With a `github.com` URL and a token with repo
access, *open PR* also opens a pull request from that branch to the repository's default branch.
Nothing is pulled after the first clone, so edits made on the remote afterwards are not seen.

**After a restart** (a controller update, for example), a task that was running on a local-model
planner resumes by itself the moment its planner node reconnects with its model loaded; there is
nothing to press. Nodes retry the controller every second for the first minute after losing it,
so this usually takes a second or two after the controller is back. Meanwhile the task shows
*Resuming* with a Stop button (in the header and next to Send), and an open task page reloads by
itself when the controller returns. Finished subtasks are kept. Work a node was still doing for
the old controller is cancelled when it reconnects, since nobody is waiting for that result, so
the resumed task does not queue behind it. If the node does not come back within 10 minutes, the
task waits for your Resume click. Tasks planned with Claude or OpenRouter, and tasks stopped at the
spending cap, always wait for your Resume click so a restart never spends money. Turn automatic
resuming off under Settings → Tasks → Running tasks.

