#!/usr/bin/env python3
# Stand-in for llama-server that writes deterministic files, to test Outpost's
# orchestration without a real model. Bug on purpose: core.py counts wrong, so
# the test-and-fix loop has something to fix.
import json, os, re, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
if "--version" in sys.argv:
    print("version: 9999 (fake)"); sys.exit(0)
port = int(sys.argv[sys.argv.index("--port") + 1])
CONTENT = {
 "wordcount/__init__.py": "from .core import count_words\n",
 "wordcount/core.py": "def count_words(text):\n    return {w: 1 for w in text.lower().split()}\n",
 "wordcount/cli.py": "import sys\nfrom .core import count_words\n\ndef main(argv):\n    with open(argv[1]) as f:\n        for w, n in sorted(count_words(f.read()).items()):\n            print(w, n)\n\nif __name__ == '__main__':\n    main(sys.argv)\n",
 "tests/test_core.py": "import unittest\nfrom wordcount.core import count_words\n\nclass T(unittest.TestCase):\n    def test_counts(self):\n        self.assertEqual(count_words('a b A'), {'a': 2, 'b': 1})\n\nif __name__ == '__main__':\n    unittest.main()\n",
 "README.md": "# Word Counter\n\nRun `python3 -m wordcount.cli FILE`.\n",
}
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        self.send_response(200); self.send_header("content-type","application/json"); self.end_headers(); self.wfile.write(b'{"status":"ok"}')
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["content-length"])))
        user = body["messages"][-1]["content"]
        with open(os.path.join(os.environ.get("OUTPOST_E2E_DIR", "."), "prompts.log"), "a") as lf:
            lf.write("=====\n" + user + "\n")
        if body.get("tools"):
            return self.agent_turn(body)
        system = body["messages"][0]["content"] if body["messages"][0]["role"] == "system" else ""
        if "reviewing a teammate" in system:
            # Review: fix the counting bug in core.py, approve everything else.
            if "wordcount/core.py" in user:
                text = "<<<FILE wordcount/core.py>>>\ndef count_words(text):\n    counts = {}\n    for w in text.lower().split():\n        counts[w] = counts.get(w, 0) + 1\n    return counts\n<<<END>>>\n"
            else:
                text = "<<<OK>>>"
        elif "Write these files:" not in user:
            text = "A hash map stores values under keys. " * 12  # the node benchmark
        else:
            files = re.search(r"Write these files: (.*)", user).group(1).split(", ")
            text = "".join(f"<<<FILE {f}>>>\n{CONTENT.get(f, '# ?\n')}<<<END>>>\n" for f in files)
        time.sleep(4)  # slow enough to watch progress and steer in the UI test
        self.send_response(200); self.send_header("content-type", "text/event-stream"); self.end_headers()
        for i in range(0, len(text), 40):
            ch = {"choices": [{"index": 0, "delta": {"content": text[i:i+40]}, "finish_reason": None}]}
            self.wfile.write(b"data: " + json.dumps(ch).encode() + b"\n\n"); self.wfile.flush(); time.sleep(0.01)
        self.wfile.write(b'data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\n')
        self.wfile.write(b'data: {"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":%d}}\n\n' % (len(text)//4))
        self.wfile.write(b"data: [DONE]\n\n")
def stream_tool_calls(self, calls, text):
    # Tool calls arrive the way llama-server sends them: name first, arguments in pieces.
    self.send_response(200); self.send_header("content-type", "text/event-stream"); self.end_headers()
    def send(delta, fin=None):
        self.wfile.write(b"data: " + json.dumps({"choices": [{"index": 0, "delta": delta, "finish_reason": fin}]}).encode() + b"\n\n"); self.wfile.flush()
    for i, (name, args) in enumerate(calls):
        a = json.dumps(args)
        send({"tool_calls": [{"index": i, "id": "call_%d" % i, "type": "function", "function": {"name": name, "arguments": ""}}]})
        send({"tool_calls": [{"index": i, "function": {"arguments": a[:12]}}]})
        send({"tool_calls": [{"index": i, "function": {"arguments": a[12:]}}]})
    if text: send({"content": text})
    send({}, "tool_calls" if calls else "stop")
    self.wfile.write(b'data: {"choices":[],"usage":{"prompt_tokens":400,"completion_tokens":40}}\n\ndata: [DONE]\n\n')

def agent_turn(self, body):
    # Investigator: search, ask the user which splitting rule, then report.
    done = sum(1 for m in body["messages"] if m["role"] == "tool")
    if done == 0: stream_tool_calls(self, [("search", {"pattern": "split"}), ("list_files", {})], "")
    elif done == 2: stream_tool_calls(self, [("ask_user", {"question": "Split words on whitespace only?", "options": ["Yes", "No"]})], "")
    else: stream_tool_calls(self, [], "Words are split on whitespace (the user confirmed: " + body["messages"][-1]["content"][-20:] + ").")
H.agent_turn = agent_turn
ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
