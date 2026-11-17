#!/usr/bin/env python3
"""Smoke checks against a REAL Outpost controller (the home lab), over its HTTP API.

Not part of CI and not the local e2e rig: it needs a running cluster. It is run by
.gitea/workflows/lab.yml on a runner inside the home network, or by hand:

    OUTPOST_LAB_URL=https://192.0.2.10:8443 OUTPOST_LAB_TOKEN=... python3 testing/lab_smoke.py

What it checks (read-only apart from one short chat message):
  1. the controller answers and reports its version and nodes
  2. "Check cluster" (the Nodes page button) on every online node: model call, command,
     file-sharing port, disk, clock, version; any failed step on an online node fails the run
  3. GET /v1/models, then one tiny chat completion through the OpenAI-compatible API

Environment:
  OUTPOST_LAB_URL        controller address, e.g. https://192.0.2.10:8443 (required)
  OUTPOST_LAB_TOKEN      an API token from Settings, API tokens (required; never printed)
  OUTPOST_LAB_FINGERPRINT optional: the controller certificate's SHA-256 (hex, colons allowed).
                         The certificate is self-signed, so without this it is not verified
                         (acceptable on a LAN, but set it when you can).
  OUTPOST_LAB_REQUIRE    optional: comma-separated node names that must be online
  OUTPOST_LAB_SKIP_CHAT  optional: 1 skips step 3 (it needs a free model slot on some node)
Exit code 0 when every check passed, 1 otherwise. Only the Python standard library is used.
"""
import hashlib
import json
import os
import socket
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

failures = []


def say(level, text):
    print(f"[{level}] {text}", flush=True)


def fail(text):
    failures.append(text)
    say("FAIL", text)


def need_env(name):
    v = os.environ.get(name, "").strip()
    if not v:
        print(f"{name} is not set. See the header of testing/lab_smoke.py.", file=sys.stderr)
        sys.exit(2)
    return v


BASE = need_env("OUTPOST_LAB_URL").rstrip("/")
if urllib.parse.urlparse(BASE).scheme not in ("http", "https") or not urllib.parse.urlparse(BASE).hostname:
    print("OUTPOST_LAB_URL must start with http:// or https:// and name the controller, "
          "for example https://192.0.2.10:8443 (the value is not shown here in case it is private).",
          file=sys.stderr)
    sys.exit(2)
TOKEN = need_env("OUTPOST_LAB_TOKEN")
FINGERPRINT = os.environ.get("OUTPOST_LAB_FINGERPRINT", "").replace(":", "").strip().lower()

# The controller's certificate is self-signed, so the normal chain check cannot pass.
CTX = ssl.create_default_context()
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE


def pin_certificate():
    u = urllib.parse.urlparse(BASE)
    if u.scheme != "https":
        return
    host, port = u.hostname, u.port or 443
    try:
        with socket.create_connection((host, port), timeout=15) as raw:
            with CTX.wrap_socket(raw, server_hostname=host) as tls:
                der = tls.getpeercert(binary_form=True)
    except OSError as e:
        fail(f"cannot connect to the controller: {e}")
        report()
    got = hashlib.sha256(der).hexdigest()
    if FINGERPRINT:
        if got != FINGERPRINT:
            fail("the controller's certificate does not match OUTPOST_LAB_FINGERPRINT "
                 f"(it is {got}); refusing to send the token")
            report()
        say("ok", "certificate fingerprint matches")
    else:
        say("warn", f"certificate not verified (set OUTPOST_LAB_FINGERPRINT to {got} to pin it)")


def call(method, path, body=None, timeout=30):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + TOKEN)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout, context=CTX) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"null")
        except ValueError:
            return e.code, None
    except (urllib.error.URLError, OSError, ValueError) as e:
        return 0, {"error": str(e)}


def report():
    print()
    if failures:
        print(f"{len(failures)} check(s) failed:")
        for f in failures:
            print("  - " + f)
        sys.exit(1)
    print("All lab checks passed.")
    sys.exit(0)


def check_state():
    code, st = call("GET", "/api/state")
    if code != 200 or not isinstance(st, dict):
        fail(f"GET /api/state returned {code}: {st} (wrong URL, or a token that is revoked or chat-only?)")
        report()
    nodes = st.get("nodes") or []
    online = [n for n in nodes if n.get("online")]
    say("ok", f"controller {st.get('version')} on {st.get('platform')}; "
              f"{len(online)} of {len(nodes)} nodes online")
    names = {n.get("name") for n in online}
    for need in filter(None, (s.strip() for s in os.environ.get("OUTPOST_LAB_REQUIRE", "").split(","))):
        if need not in names:
            fail(f"required node '{need}' is not online")
    for n in nodes:
        if not n.get("online"):
            say("info", f"node '{n.get('name')}' is offline (fine unless it is in OUTPOST_LAB_REQUIRE)")
    return st


def check_cluster():
    code, res = call("POST", "/api/cluster/check")
    if code == 409:
        say("info", "a cluster check was already running; waiting for it")
    elif code != 200:
        fail(f"starting Check cluster returned {code}: {res}")
        return
    deadline = time.time() + 300
    while time.time() < deadline:
        code, res = call("GET", "/api/cluster/check")
        if code == 200 and isinstance(res, dict) and not res.get("running"):
            break
        time.sleep(3)
    else:
        fail("Check cluster did not finish within 5 minutes")
        return
    for row in res.get("rows") or []:
        name = row.get("name") or row.get("id")
        if not row.get("online"):
            say("info", f"{name}: offline, not checked")
            continue
        bad = []
        for step, s in (row.get("steps") or {}).items():
            state = s.get("state")
            if state == "fail":
                bad.append(f"{step}: {s.get('text')}")
            elif state == "warn":
                say("warn", f"{name} {step}: {s.get('text')}")
        for note in row.get("notes") or []:
            if note.get("level") == "bad":
                bad.append(note.get("text", ""))
        if bad:
            fail(f"{name}: " + "; ".join(bad))
        else:
            say("ok", f"{name}: all steps passed")


def check_chat():
    if os.environ.get("OUTPOST_LAB_SKIP_CHAT") == "1":
        say("info", "chat check skipped (OUTPOST_LAB_SKIP_CHAT=1)")
        return
    code, res = call("GET", "/v1/models")
    if code != 200:
        fail(f"GET /v1/models returned {code}: {res}")
        return
    models = [m.get("id") for m in (res.get("data") or [])]
    say("ok", f"/v1/models lists {len(models)} model(s): {', '.join(m for m in models if m)[:200]}")
    code, res = call("POST", "/v1/chat/completions", {
        "model": "auto", "max_tokens": 32, "temperature": 0,
        "messages": [{"role": "user", "content": "Reply with the single word: ok"}],
    }, timeout=180)
    if code != 200:
        fail(f"chat completion returned {code}: {res} (no model loaded, or every node busy?)")
        return
    try:
        text = res["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        fail(f"chat completion had no answer: {res}")
        return
    if not (text or "").strip():
        fail("chat completion answered with empty text")
    else:
        say("ok", f"chat completion answered: {text.strip()[:80]!r}")


if __name__ == "__main__":
    pin_certificate()
    check_state()
    check_cluster()
    check_chat()
    report()
