#!/usr/bin/env python3
"""Compare planners on vague requests, against a REAL Outpost controller. Not part of CI.

For each of eight deliberately vague requests it starts a task twice, once with the local planner ("auto": the
best ready node) and once with Claude (when a Claude key is saved), waits for the first thing the planner
produces (a plan, or questions), records it, and then stops and deletes the task. It never approves a plan, so
no node writes any code. Claude spends a few cents (each call is capped at $0.50 per task).

It writes one HTML report, lab-assume-report.html (or OUTPOST_LAB_OUT): for every request and planner, whether
it planned at once or asked, its assumptions, how long it took, and the plan's summary.

    OUTPOST_LAB_URL=https://192.0.2.10:8443 OUTPOST_LAB_TOKEN=... python3 testing/lab_assume.py

Environment:
  OUTPOST_LAB_URL        controller address, https:// (required)
  OUTPOST_LAB_TOKEN      an API token from Settings, API tokens (required; never printed)
  OUTPOST_LAB_FINGERPRINT optional: the controller certificate's SHA-256 (hex, colons allowed); without it the
                         self-signed certificate is not verified (acceptable on a LAN)
  OUTPOST_LAB_PLANNERS   optional, default "auto,claude": comma-separated planners; "claude" is skipped when no key
  OUTPOST_LAB_TIMEOUT    optional, seconds to wait for each planner answer, default 900
  OUTPOST_LAB_OUT        optional, report path, default ./lab-assume-report.html
Only the Python standard library is used. Exit code 0 when every run produced a plan or questions.
"""
import hashlib
import html
import json
import os
import socket
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

REQUESTS = [
    "A tool to track the books I read.",
    "Back up my photos.",
    "A small API for notes.",
    "Something that tells me when my website is down.",
    "A budget app for two people.",
    "Clean up my downloads folder automatically.",
    "A quiz game for kids.",
    "A script that renames my music files properly.",
]


def need_env(name):
    v = os.environ.get(name, "").strip()
    if not v:
        print(f"{name} is not set. See the header of testing/lab_assume.py.", file=sys.stderr)
        sys.exit(2)
    return v


BASE = need_env("OUTPOST_LAB_URL").rstrip("/")
u = urllib.parse.urlparse(BASE)
if u.scheme not in ("http", "https") or not u.hostname:
    print("OUTPOST_LAB_URL must start with http:// or https:// and name the controller, for example "
          "https://192.0.2.10:8443 (the value is not shown here in case it is private).", file=sys.stderr)
    sys.exit(2)
TOKEN = need_env("OUTPOST_LAB_TOKEN")
FINGERPRINT = os.environ.get("OUTPOST_LAB_FINGERPRINT", "").replace(":", "").strip().lower()
TIMEOUT = int(os.environ.get("OUTPOST_LAB_TIMEOUT", "900"))
OUT = os.environ.get("OUTPOST_LAB_OUT", "lab-assume-report.html")
PLANNERS = [p.strip() for p in os.environ.get("OUTPOST_LAB_PLANNERS", "auto,claude").split(",") if p.strip()]

CTX = ssl.create_default_context()
CTX.check_hostname = False
CTX.verify_mode = ssl.CERT_NONE


def pin_certificate():
    if u.scheme != "https" or not FINGERPRINT:
        return
    with socket.create_connection((u.hostname, u.port or 443), timeout=15) as raw:
        with CTX.wrap_socket(raw, server_hostname=u.hostname) as tls:
            got = hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest()
    if got != FINGERPRINT:
        print("The controller's certificate does not match OUTPOST_LAB_FINGERPRINT.", file=sys.stderr)
        sys.exit(2)


def call(method, path, body=None, timeout=60):
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


def first_answer(task):
    """What the planner produced so far: ('plan', msg) | ('questions', msg) | ('failed', text) | None."""
    status = task.get("status")
    for m in task.get("messages") or []:
        if m.get("kind") == "plan" and m.get("plan"):
            return "plan", m
        if m.get("kind") == "questions":
            return "questions", m
    if status == "failed":
        return "failed", task.get("error") or "failed"
    return None


def run_one(prompt, planner):
    t0 = time.time()
    code, res = call("POST", "/api/tasks", {"prompt": prompt, "planner": planner, "cap_usd": 0.5, "review": False})
    if code != 200 or not isinstance(res, dict) or not (res.get("id") or (res.get("task") or {}).get("id")):
        return {"outcome": "error", "text": f"could not start the task ({code}): {res}", "seconds": 0}
    tid = res.get("id") or res["task"]["id"]
    out = {"outcome": "timeout", "text": f"no answer within {TIMEOUT} s", "seconds": TIMEOUT}
    while time.time() - t0 < TIMEOUT:
        time.sleep(3)
        code, task = call("GET", "/api/tasks/" + tid)
        if code != 200 or not isinstance(task, dict):
            continue
        ans = first_answer(task)
        if ans:
            kind, payload = ans
            secs = round(time.time() - t0)
            if kind == "plan":
                p = payload["plan"]
                out = {"outcome": "plan", "seconds": secs, "summary": p.get("summary", ""), "language": p.get("language", ""),
                       "assumptions": p.get("assumptions") or [], "subtasks": len(p.get("subtasks") or []), "note": payload.get("text", "")}
            elif kind == "questions":
                out = {"outcome": "questions", "seconds": secs, "questions": [q.get("question", "") for q in payload.get("questions") or []],
                       "note": payload.get("text", "")}
            else:
                out = {"outcome": "failed", "seconds": secs, "text": str(payload)}
            break
    call("POST", f"/api/tasks/{tid}/cancel")  # never approved; stop whatever is still going, then remove it
    time.sleep(1)
    call("DELETE", f"/api/tasks/{tid}")
    return out


def esc(s):
    return html.escape(str(s))


def cell(r):
    if r["outcome"] == "plan":
        asm = "".join(f"<li><b>{esc(a['topic'])}:</b> {esc(a['choice'])} <span class='m'>({esc(', '.join(a.get('options') or []))})</span></li>" for a in r["assumptions"])
        head = f"<span class='ok'>planned at once</span> · {r['seconds']} s · {r['subtasks']} subtasks"
        body = f"<ul>{asm}</ul>" if asm else "<p class='m'>no assumptions listed</p>"
        return f"{head}{body}<p class='m'>{esc(r.get('summary', '')[:300])}</p>"
    if r["outcome"] == "questions":
        qs = "".join(f"<li>{esc(q)}</li>" for q in r["questions"])
        return f"<span class='ask'>asked first</span> · {r['seconds']} s<ul>{qs}</ul>"
    return f"<span class='bad'>{esc(r['outcome'])}</span> · {esc(r.get('text', ''))}"


def write_report(rows, planners):
    head = "".join(f"<th>{esc(p)}</th>" for p in planners)
    body = "".join(f"<tr><td class='req'>{esc(req)}</td>" + "".join(f"<td>{cell(res[p])}</td>" for p in planners) + "</tr>" for req, res in rows)
    page = f"""<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Plan with assumptions: lab comparison</title><style>
:root{{--bg:#f6f5f1;--card:#fff;--ink:#1d1d1b;--mut:#6b6a65;--line:#e3e1da;--ok:#1a7a43;--ask:#93600f;--bad:#c0392b}}
@media(prefers-color-scheme:dark){{:root{{--bg:#161614;--card:#1f1f1c;--ink:#ecebe6;--mut:#a09f98;--line:#33332f;--ok:#3ba86a;--ask:#d9a441;--bad:#e5675a}}}}
body{{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 system-ui,sans-serif}}.w{{max-width:1300px;margin:0 auto;padding:24px 16px 60px;overflow-x:auto}}
table{{border-collapse:collapse;width:100%;background:var(--card);border:1px solid var(--line)}}td,th{{border-top:1px solid var(--line);padding:10px;text-align:left;vertical-align:top}}
.req{{width:16%;font-weight:550}}.m{{color:var(--mut)}}.ok{{color:var(--ok);font-weight:550}}.ask{{color:var(--ask);font-weight:550}}.bad{{color:var(--bad);font-weight:550}}ul{{margin:6px 0;padding-left:18px}}
</style></head><body><div class="w"><h1>Plan with assumptions: lab comparison</h1>
<p class="m">Eight vague requests, planned once per planner and never approved. Look for: sensible assumptions, no broken output, asking only when it truly cannot guess.</p>
<table><thead><tr><th>Request</th>{head}</tr></thead><tbody>{body}</tbody></table></div></body></html>"""
    with open(OUT, "w") as f:
        f.write(page)


def main():
    pin_certificate()
    code, st = call("GET", "/api/state")
    if code != 200 or not isinstance(st, dict):
        print(f"cannot read the controller state ({code})", file=sys.stderr)
        sys.exit(2)
    planners = []
    for p in PLANNERS:
        if p == "claude" and not st.get("has_claude_key"):
            print("[info] no Claude key saved: skipping the Claude column")
            continue
        planners.append(p)
    if not planners:
        print("no planner to compare", file=sys.stderr)
        sys.exit(2)
    rows, bad = [], 0
    for req in REQUESTS:
        res = {}
        for p in planners:
            print(f"[run] {p}: {req}", flush=True)
            res[p] = run_one(req, p)
            print(f"[{res[p]['outcome']}] {p}: {res[p].get('seconds', 0)} s", flush=True)
            if res[p]["outcome"] not in ("plan", "questions"):
                bad += 1
        rows.append((req, res))
        write_report(rows, planners)  # keep a partial report if the run is interrupted
    print(f"Report written to {OUT}.")
    if bad:
        print(f"{bad} run(s) produced no plan or questions.")
        sys.exit(1)


main()
