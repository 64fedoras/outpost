# Stub of the Claude Messages API for testing Outpost's planner, cost meter and cap.
import json, re, sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PLAN = {
  "title": "Word Counter",
  "summary": "A tiny Python CLI. wordcount/core.py defines count_words(text: str) -> dict[str, int] (lowercase, split on whitespace). wordcount/cli.py has main(argv) that reads a file path and prints 'word count' lines sorted by word.",
  "language": "python",
  "setup_command": "",
  "test_command": "python3 -m unittest discover -s tests -v",
  "subtasks": [
    {"id": "t1", "title": "Core counting", "files": ["wordcount/__init__.py", "wordcount/core.py"], "depends_on": [],
     "instructions": "Define count_words(text) returning a dict of lowercase word -> count."},
    {"id": "t2", "title": "CLI", "files": ["wordcount/cli.py"], "depends_on": ["t1"],
     "instructions": "main(argv) reads argv[1], calls count_words, prints lines."},
    {"id": "t3", "title": "Tests and README", "files": ["tests/test_core.py", "README.md"], "depends_on": ["t1"],
     "instructions": "unittest tests for count_words; README explains usage."}
  ]
}
calls = {"n": 0}

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["content-length"])))
        if self.headers.get("x-api-key") != "sk-test-key":
            return self.send(401, {"type": "error", "error": {"type": "authentication_error", "message": "invalid x-api-key"}})
        calls["n"] += 1
        system = body["system"][0]["text"]
        user = body["messages"][0]["content"]
        if "planner for Outpost" in system:
            if "ask me first" in user and "already asked your questions" not in user and "User: Language" not in user and "answered" not in user.lower() and "→" not in user:
                text = json.dumps({"questions": [
                    {"id": "q1", "question": "Which language should it use?", "options": ["Python", "Go"]},
                    {"id": "q2", "question": "How should words be counted?", "options": ["Case-insensitive", "Case-sensitive"]},
                    {"id": "q3", "question": "Extra outputs?", "options": ["JSON output", "Top-N limit", "None"], "multi": True}],
                    "note": "Two choices change how the code is structured."})
            elif "follow-up request on an existing project" in user:
                text = json.dumps({"title": "Word Counter", "summary": PLAN["summary"] + " cli.py also supports --top N.",
                    "language": "python", "setup_command": "", "test_command": PLAN["test_command"],
                    "note": "Only the CLI and its README change.",
                    "subtasks": [{"id": "f1", "title": "Add --top N to the CLI", "files": ["wordcount/cli.py", "README.md"], "depends_on": [],
                                  "instructions": "Add a --top N option that prints only the N most frequent words."}]})
            else:
                plan = dict(PLAN); plan["note"] = "I went with a small Python package and unittest, since you asked for standard tools."
                text = json.dumps(plan)
        elif "tests fail" in system:
            # Fix: make core.py correct.
            text = "<<<FILE wordcount/core.py>>>\ndef count_words(text):\n    counts = {}\n    for w in text.lower().split():\n        counts[w] = counts.get(w, 0) + 1\n    return counts\n<<<END>>>\n"
        else:
            files = re.search(r"Write these files: (.*)", user).group(1).split(", ")
            text = "".join(f"<<<FILE {f}>>>\n# written by the planner\n<<<END>>>\n" for f in files)
        self.send(200, {"id": "msg_1", "type": "message", "role": "assistant", "model": body["model"],
            "content": [{"type": "text", "text": text}], "stop_reason": "end_turn",
            "usage": {"input_tokens": 1200, "output_tokens": 600, "cache_creation_input_tokens": 800, "cache_read_input_tokens": 0}})
    def send(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("content-type", "application/json"); self.send_header("content-length", str(len(b))); self.end_headers(); self.wfile.write(b)

ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
