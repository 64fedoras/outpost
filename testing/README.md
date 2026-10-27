# Testing

## Unit tests
    go vet ./... && go test ./...

## End-to-end rig (no model, no API key)
`testing/e2e/run.sh up` builds with `-tags e2e`, starts a real controller and a real node on
127.0.0.1, a stub Claude API (`mock_claude.py`, key `sk-test-key`) and a fake llama-server
(`fake_llama_server.py`, shipped inside the stub llama.cpp archive in `fixtures/ctl-seed`).
It enables exec on the node and loads the 1 MiB `tiny-test` model. State is in
`testing/e2e/run/` (git-ignored); `run.sh down` stops everything, `run.sh pw` prints the
admin password. UI: https://127.0.0.1:18460 (self-signed).

Then, with `OUTPOST_E2E_DIR=$PWD/testing/e2e/run`:

| Script | What it does |
| --- | --- |
| `flow.py dark\|light` | Full chat flow: question cards, plan approval, steering note, result, follow-up round. About 3 to 4 minutes, because the fake model sleeps 4 s per call. Screenshots go to `run/chat/`. |
| `mobile.py` | The same pages at phone width. |
| `shots.py` | Screenshots of every tab (needs a task id edited into the script). |

The fake model writes a deliberate bug in `wordcount/core.py` so the review and test-fix loop
has something to fix. `mock_claude.py` returns questions when the prompt contains "ask me
first", a one-subtask plan for follow-ups, and a fixed 3-subtask plan otherwise.

Playwright and Chromium are expected to be installed (`pip install playwright && playwright install chromium`).

## Not covered by any script (test by hand)
- Windows: tray icon, install, web-UI update (needs a real Windows machine; Defender behaviour).
- Real GPUs and Vulkan/ROCm builds of llama.cpp, memory pooling across machines.
- macOS (builds only).
- Real Claude API cost figures.
